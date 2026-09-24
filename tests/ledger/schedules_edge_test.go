package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func feSchedule(t *testing.T, e *env, in ledger.ScheduleInput) ledger.ScheduledTransaction {
	t.Helper()
	st, err := e.m.Schedule(context.Background(), in)
	if err != nil {
		t.Fatalf("Schedule(%s) error = %v", in.IdempotencyKey, err)
	}
	return st
}

func feScheduled(t *testing.T, e *env, id uuid.UUID) ledger.ScheduledTransaction {
	t.Helper()
	st, err := e.m.Scheduled(context.Background(), id)
	if err != nil {
		t.Fatalf("Scheduled() error = %v", err)
	}
	return st
}

func feDrain(t *testing.T, e *env) (executed, failed int) {
	t.Helper()
	for {
		x, f, err := e.m.ExecuteDue(context.Background())
		if err != nil {
			t.Fatalf("ExecuteDue() error = %v", err)
		}
		if x+f == 0 {
			return executed, failed
		}
		executed, failed = executed+x, failed+f
	}
}

func TestSchedulesEdgeValidation(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	soon := time.Now().Add(time.Hour)

	tests := []struct {
		name   string
		mutate func(*ledger.ScheduleInput)
		want   error
	}{
		{"no execute_at", func(in *ledger.ScheduleInput) { in.ExecuteAt = time.Time{} }, ledger.ErrInvalid},
		{"no key", func(in *ledger.ScheduleInput) { in.IdempotencyKey = "" }, ledger.ErrInvalid},
		{"one posting", func(in *ledger.ScheduleInput) { in.Postings = in.Postings[:1] }, ledger.ErrInvalid},
		{"zero amount", func(in *ledger.ScheduleInput) { in.Postings[0].Amount = amt(0) }, ledger.ErrInvalid},
		{"archived status", func(in *ledger.ScheduleInput) { in.Status = ledger.TransactionArchived }, ledger.ErrInvalid},
		{"metadata not an object", func(in *ledger.ScheduleInput) { in.Metadata = jsontext.Value(`"x"`) }, ledger.ErrInvalid},
		{"unknown account", func(in *ledger.ScheduleInput) { in.Postings[0].AccountID = uuid.New() }, ledger.ErrNotFound},
		{"unbalanced is accepted until execution", func(in *ledger.ScheduleInput) { in.Postings[0].Amount = amt(2) }, nil},
		{"same account twice", func(in *ledger.ScheduleInput) { in.Postings[0].AccountID = in.Postings[1].AccountID }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := scheduleInput("v:"+uuid.NewString(), a.ID, b.ID, 1, soon)
			in.Postings = append([]ledger.Posting(nil), in.Postings...)
			tt.mutate(&in)
			st, err := e.m.Schedule(ctx, in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Schedule() error = %v, want %v", err, tt.want)
			}
			if err == nil && (st.Status != ledger.ScheduleScheduled || st.ResolvedAt != nil || st.TransactionID != nil) {
				t.Fatalf("schedule = %+v", st)
			}
		})
	}
}

func TestSchedulesEdgeExecutionOutcomes(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	rich := e.funded(t, 1_000)
	poor := e.funded(t, 10)
	b := e.account(t, "USD", ledger.Debit)
	closed := e.account(t, "USD", ledger.Debit)
	if _, err := e.m.CloseAccount(ctx, closed.ID); err != nil {
		t.Fatal(err)
	}
	existing := e.post(t, transfer("already-posted", rich.ID, b.ID, 5))
	past := time.Now().Add(-time.Minute)

	tests := []struct {
		name      string
		in        ledger.ScheduleInput
		status    ledger.ScheduleStatus
		failure   string
		txnStatus ledger.TransactionStatus
		txnID     *uuid.UUID
	}{
		{"due", scheduleInput("due", rich.ID, b.ID, 1, past), ledger.ScheduleExecuted, "", ledger.TransactionPosted, nil},
		{"far past", scheduleInput("far-past", rich.ID, b.ID, 1, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)), ledger.ScheduleExecuted, "", ledger.TransactionPosted, nil},
		{"future", scheduleInput("future", rich.ID, b.ID, 1, time.Now().Add(time.Hour)), ledger.ScheduleScheduled, "", "", nil},
		{"insufficient funds", scheduleInput("poor", poor.ID, b.ID, 11, past), ledger.ScheduleFailed, ledger.ErrInsufficientFunds.Error(), "", nil},
		{"exactly the balance", scheduleInput("exact", poor.ID, b.ID, 10, past.Add(time.Second)), ledger.ScheduleExecuted, "", ledger.TransactionPosted, nil},
		{"closed account", scheduleInput("closed", rich.ID, closed.ID, 1, past), ledger.ScheduleFailed, ledger.ErrAccountNotOpen.Error(), "", nil},
		{"unbalanced", func() ledger.ScheduleInput {
			in := scheduleInput("unbalanced", rich.ID, b.ID, 1, past)
			in.Postings[0].Amount = amt(2)
			return in
		}(), ledger.ScheduleFailed, ledger.ErrUnbalanced.Error(), "", nil},
		{"pending status", func() ledger.ScheduleInput {
			in := scheduleInput("pending", rich.ID, b.ID, 3, past)
			in.Status = ledger.TransactionPending
			return in
		}(), ledger.ScheduleExecuted, "", ledger.TransactionPending, nil},
		{"key of an identical posted transaction", scheduleInput("already-posted", rich.ID, b.ID, 5, past), ledger.ScheduleExecuted, "", ledger.TransactionPosted, &existing.ID},
	}
	ids := make([]uuid.UUID, len(tests))
	for i, tt := range tests {
		ids[i] = feSchedule(t, e, tt.in).ID
	}

	executed, failed := feDrain(t, e)
	if executed != 5 || failed != 3 {
		t.Fatalf("ExecuteDue() executed %d failed %d, want 5 and 3", executed, failed)
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := feScheduled(t, e, ids[i])
			if st.Status != tt.status {
				t.Fatalf("status = %s, want %s (failure %v)", st.Status, tt.status, st.Failure)
			}
			switch tt.status {
			case ledger.ScheduleScheduled:
				if st.ResolvedAt != nil || st.TransactionID != nil || st.Failure != nil {
					t.Fatalf("unresolved schedule = %+v", st)
				}
			case ledger.ScheduleFailed:
				if st.ResolvedAt == nil || st.TransactionID != nil || st.Failure == nil || !strings.Contains(*st.Failure, tt.failure) {
					t.Fatalf("failed schedule = %+v, want failure containing %q", st, tt.failure)
				}
			case ledger.ScheduleExecuted:
				if st.ResolvedAt == nil || st.TransactionID == nil || st.Failure != nil {
					t.Fatalf("executed schedule = %+v", st)
				}
				if tt.txnID != nil && *st.TransactionID != *tt.txnID {
					t.Fatalf("transaction = %s, want the existing %s", *st.TransactionID, *tt.txnID)
				}
				txn, err := e.m.Transaction(ctx, *st.TransactionID)
				if err != nil || txn.Status != tt.txnStatus || txn.IdempotencyKey != st.IdempotencyKey {
					t.Fatalf("transaction = %+v, %v", txn, err)
				}
			}
		})
	}
	if e.balance(t, poor.ID) != 0 || e.balance(t, closed.ID) != 0 {
		t.Fatal("failed schedules moved money")
	}
	e.verify(t)
}

func TestSchedulesEdgeKeyConflictWithExistingSchedule(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	e.post(t, transfer("posted-key", a.ID, b.ID, 5))

	st := feSchedule(t, e, scheduleInput("posted-key", a.ID, b.ID, 6, time.Now().Add(-time.Minute)))
	if x, f := feDrain(t, e); x != 0 || f != 1 {
		t.Fatalf("executed %d failed %d, want 0 and 1", x, f)
	}
	got := feScheduled(t, e, st.ID)
	if got.Status != ledger.ScheduleFailed || got.Failure == nil || !strings.Contains(*got.Failure, ledger.ErrIdempotencyConflict.Error()) {
		t.Fatalf("schedule = %+v", got)
	}
	if e.balance(t, b.ID) != 5 {
		t.Fatalf("b = %d, want 5", e.balance(t, b.ID))
	}
	_, err := e.m.CancelSchedule(ctx, st.ID)
	wantErr(t, err, ledger.ErrScheduleNotPending)
}

func TestSchedulesEdgeEffectiveAtIsPreserved(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	in := scheduleInput("backdated", a.ID, b.ID, 7, time.Now().Add(-time.Minute))
	in.EffectiveAt = new(day(3))
	in.ExternalID = "ext-backdated"
	st := feSchedule(t, e, in)
	feDrain(t, e)
	st = feScheduled(t, e, st.ID)
	if st.TransactionID == nil {
		t.Fatalf("schedule = %+v", st)
	}
	txn, err := e.m.Transaction(ctx, *st.TransactionID)
	if err != nil || !txn.EffectiveAt.Equal(day(3)) || txn.ExternalID != "ext-backdated" {
		t.Fatalf("transaction = %+v, %v", txn, err)
	}
	b2, err := e.m.Balances(ctx, b.ID, ledger.EffectiveRange{Until: new(day(4))})
	if err != nil || b2.Posted.Amount != amt(7) {
		t.Fatalf("balance by day 4 = %+v, %v", b2, err)
	}
}

func TestSchedulesEdgeReplay(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	when := time.Now().Add(time.Hour).Truncate(time.Microsecond)

	tests := []struct {
		name   string
		mutate func(*ledger.ScheduleInput)
	}{
		{"plain", func(in *ledger.ScheduleInput) {}},
		{"metadata", func(in *ledger.ScheduleInput) { in.Metadata = jsontext.Value(`{"a": 1}`) }},
		{"effective_at", func(in *ledger.ScheduleInput) { in.EffectiveAt = new(day(2)) }},
		{"pending status", func(in *ledger.ScheduleInput) { in.Status = ledger.TransactionPending }},
		{"external_id", func(in *ledger.ScheduleInput) { in.ExternalID = "ext-" + in.IdempotencyKey }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := scheduleInput("replay-"+strings.ReplaceAll(tt.name, " ", "-"), a.ID, b.ID, 1, when)
			tt.mutate(&in)
			first := feSchedule(t, e, in)
			again, err := e.m.Schedule(ctx, in)
			if err != nil || again.ID != first.ID {
				t.Fatalf("identical replay = %+v, %v; want the original schedule", again.ID, err)
			}
		})
	}

	t.Run("sub-microsecond execute_at is the same schedule", func(t *testing.T) {
		in := scheduleInput("replay-nanos", a.ID, b.ID, 1, when.Add(300*time.Nanosecond))
		first := feSchedule(t, e, in)
		in.ExecuteAt = when.Add(700 * time.Nanosecond)
		again, err := e.m.Schedule(ctx, in)
		if err != nil || again.ID != first.ID || !first.ExecuteAt.Equal(when) {
			t.Fatalf("replay = %+v, %v; first execute_at %s", again, err, first.ExecuteAt)
		}
	})

	t.Run("changed terms conflict", func(t *testing.T) {
		base := scheduleInput("replay-conflict", a.ID, b.ID, 1, when)
		feSchedule(t, e, base)
		for name, mutate := range map[string]func(*ledger.ScheduleInput){
			"amount":      func(in *ledger.ScheduleInput) { in.Postings[0].Amount, in.Postings[1].Amount = amt(2), amt(2) },
			"description": func(in *ledger.ScheduleInput) { in.Description = "other" },
			"metadata":    func(in *ledger.ScheduleInput) { in.Metadata = jsontext.Value(`{"x":"y"}`) },
			"execute_at":  func(in *ledger.ScheduleInput) { in.ExecuteAt = when.Add(time.Microsecond) },
			"direction":   func(in *ledger.ScheduleInput) { in.Postings[0].Side, in.Postings[1].Side = ledger.Credit, ledger.Debit },
		} {
			in := base
			in.Postings = append([]ledger.Posting(nil), base.Postings...)
			mutate(&in)
			_, err := e.m.Schedule(ctx, in)
			if !errors.Is(err, ledger.ErrIdempotencyConflict) {
				t.Errorf("%s: error = %v, want ErrIdempotencyConflict", name, err)
			}
		}
	})

	t.Run("replay after execution returns the executed schedule", func(t *testing.T) {
		in := scheduleInput("replay-executed", a.ID, b.ID, 1, time.Now().Add(-time.Minute).Truncate(time.Microsecond))
		first := feSchedule(t, e, in)
		feDrain(t, e)
		again, err := e.m.Schedule(ctx, in)
		if err != nil || again.ID != first.ID || again.Status != ledger.ScheduleExecuted {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		if x, f := feDrain(t, e); x+f != 0 {
			t.Fatalf("replay re-queued the schedule: %d executed %d failed", x, f)
		}
	})
}

func TestSchedulesEdgeCancel(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	t.Run("twice is idempotent", func(t *testing.T) {
		st := feSchedule(t, e, scheduleInput("cancel-twice", a.ID, b.ID, 1, time.Now().Add(time.Hour)))
		first, err := e.m.CancelSchedule(ctx, st.ID)
		if err != nil || first.Status != ledger.ScheduleCanceled || first.ResolvedAt == nil {
			t.Fatalf("cancel = %+v, %v", first, err)
		}
		second, err := e.m.CancelSchedule(ctx, st.ID)
		if err != nil || second.Status != ledger.ScheduleCanceled || !second.ResolvedAt.Equal(*first.ResolvedAt) {
			t.Fatalf("second cancel = %+v, %v", second, err)
		}
	})

	t.Run("a canceled due schedule never runs", func(t *testing.T) {
		st := feSchedule(t, e, scheduleInput("cancel-due", a.ID, b.ID, 1, time.Now().Add(-time.Minute)))
		if _, err := e.m.CancelSchedule(ctx, st.ID); err != nil {
			t.Fatal(err)
		}
		if x, f := feDrain(t, e); x+f != 0 {
			t.Fatalf("executed %d failed %d, want nothing", x, f)
		}
		if got := feScheduled(t, e, st.ID); got.Status != ledger.ScheduleCanceled || got.TransactionID != nil {
			t.Fatalf("schedule = %+v", got)
		}
	})

	t.Run("after execution or failure", func(t *testing.T) {
		done := feSchedule(t, e, scheduleInput("cancel-done", a.ID, b.ID, 1, time.Now().Add(-time.Minute)))
		broke := feSchedule(t, e, scheduleInput("cancel-broke", a.ID, b.ID, 1_000, time.Now().Add(-time.Minute)))
		feDrain(t, e)
		for _, id := range []uuid.UUID{done.ID, broke.ID} {
			before := feScheduled(t, e, id)
			_, err := e.m.CancelSchedule(ctx, id)
			wantErr(t, err, ledger.ErrScheduleNotPending)
			if after := feScheduled(t, e, id); after.Status != before.Status {
				t.Fatalf("cancel changed status %s to %s", before.Status, after.Status)
			}
		}
	})

	t.Run("racing cancel and execute", func(t *testing.T) {
		for i := range 8 {
			st := feSchedule(t, e, scheduleInput(fmt.Sprint("race-cancel-", i), e.open.ID, b.ID, 1, time.Now().Add(-time.Minute)))
			var (
				wg        sync.WaitGroup
				cancelErr error
				execErr   error
			)
			wg.Go(func() { _, cancelErr = e.m.CancelSchedule(ctx, st.ID) })
			wg.Go(func() { _, _, execErr = e.m.ExecuteDue(ctx) })
			wg.Wait()
			if execErr != nil {
				t.Fatal(execErr)
			}
			feDrain(t, e)
			got := feScheduled(t, e, st.ID)
			switch got.Status {
			case ledger.ScheduleCanceled:
				if cancelErr != nil || got.TransactionID != nil {
					t.Fatalf("round %d: canceled schedule %+v, cancel error %v", i, got, cancelErr)
				}
			case ledger.ScheduleExecuted:
				if !errors.Is(cancelErr, ledger.ErrScheduleNotPending) {
					t.Fatalf("round %d: executed but cancel returned %v", i, cancelErr)
				}
			default:
				t.Fatalf("round %d: status %s", i, got.Status)
			}
		}
	})
	e.verify(t)
}

func TestSchedulesEdgeSweepBatches(t *testing.T) {
	t.Parallel()
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, SweepBatch: 2})
	ctx := context.Background()
	a := e.funded(t, 3)
	b := e.account(t, "USD", ledger.Debit)
	base := time.Now().Add(-time.Hour)

	var sts []ledger.ScheduledTransaction
	for i := range 5 {
		sts = append(sts, feSchedule(t, e, scheduleInput(fmt.Sprint("sb-", i), a.ID, b.ID, 1, base.Add(time.Duration(i)*time.Minute))))
	}
	want := [][2]int{{2, 0}, {1, 1}, {0, 1}, {0, 0}}
	for i, w := range want {
		x, f, err := e.m.ExecuteDue(ctx)
		if err != nil || x != w[0] || f != w[1] {
			t.Fatalf("sweep %d = %d executed %d failed %v, want %v", i, x, f, err, w)
		}
	}
	for i, st := range sts {
		wantStatus := ledger.ScheduleExecuted
		if i >= 3 {
			wantStatus = ledger.ScheduleFailed
		}
		if got := feScheduled(t, e, st.ID); got.Status != wantStatus {
			t.Fatalf("schedule %d = %s, want %s (earliest first)", i, got.Status, wantStatus)
		}
	}
	e.verify(t)
}

func TestSchedulesEdgeConcurrentSweepsRunEachOnce(t *testing.T) {
	t.Parallel()
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, SweepBatch: 4})
	ctx := context.Background()
	a := e.funded(t, 100_000)
	b := e.account(t, "USD", ledger.Debit)
	const total = 40
	for i := range total {
		feSchedule(t, e, scheduleInput(fmt.Sprint("conc-", i), a.ID, b.ID, int64(i+1), time.Now().Add(-time.Minute)))
	}

	var (
		wg               sync.WaitGroup
		mu               sync.Mutex
		executed, failed int
	)
	for range 8 {
		wg.Go(func() {
			for {
				x, f, err := e.m.ExecuteDue(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if x+f == 0 {
					return
				}
				mu.Lock()
				executed, failed = executed+x, failed+f
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	feDrain(t, e)
	if executed != total || failed != 0 {
		t.Fatalf("executed %d failed %d, want %d and 0", executed, failed, total)
	}
	if got := e.balance(t, b.ID); got != total*(total+1)/2 {
		t.Fatalf("b = %d, want %d", got, total*(total+1)/2)
	}
	list, err := e.m.ListSchedules(ctx, ledger.ListSchedulesInput{Status: ledger.ScheduleExecuted, Limit: 1000})
	if err != nil || len(list) != total {
		t.Fatalf("executed schedules = %d, %v", len(list), err)
	}
	seen := map[uuid.UUID]bool{}
	for _, st := range list {
		if st.TransactionID == nil || seen[*st.TransactionID] {
			t.Fatalf("schedule %s has transaction %v (duplicate or missing)", st.ID, st.TransactionID)
		}
		seen[*st.TransactionID] = true
	}
	e.verify(t)
}

func TestSchedulesEdgeList(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)

	executed := feSchedule(t, e, scheduleInput("ls-executed", a.ID, b.ID, 1, past))
	feSchedule(t, e, scheduleInput("ls-failed", a.ID, b.ID, 1_000, past))
	canceled := feSchedule(t, e, scheduleInput("ls-canceled", a.ID, b.ID, 1, future))
	waiting1 := feSchedule(t, e, scheduleInput("ls-waiting-1", a.ID, b.ID, 1, future))
	waiting2 := feSchedule(t, e, scheduleInput("ls-waiting-2", a.ID, b.ID, 1, future))
	feDrain(t, e)
	if _, err := e.m.CancelSchedule(ctx, canceled.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(sts []ledger.ScheduledTransaction) string {
		out := make([]string, len(sts))
		for i, st := range sts {
			out[i] = st.IdempotencyKey
		}
		return strings.Join(out, ",")
	}
	all := "ls-waiting-2,ls-waiting-1,ls-canceled,ls-failed,ls-executed"
	tests := []struct {
		name string
		in   ledger.ListSchedulesInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListSchedulesInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListSchedulesInput{Limit: -5}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListSchedulesInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"bad status", ledger.ListSchedulesInput{Status: "done", Limit: 10}, "", ledger.ErrInvalid},
		{"limit at max", ledger.ListSchedulesInput{Limit: 1000}, all, nil},
		{"limit one", ledger.ListSchedulesInput{Limit: 1}, "ls-waiting-2", nil},
		{"exact page", ledger.ListSchedulesInput{Limit: 5}, all, nil},
		{"after exact page", ledger.ListSchedulesInput{Before: executed.ID, Limit: 5}, "", nil},
		{"middle", ledger.ListSchedulesInput{Before: waiting1.ID, Limit: 2}, "ls-canceled,ls-failed", nil},
		{"scheduled", ledger.ListSchedulesInput{Status: ledger.ScheduleScheduled, Limit: 10}, "ls-waiting-2,ls-waiting-1", nil},
		{"executed", ledger.ListSchedulesInput{Status: ledger.ScheduleExecuted, Limit: 10}, "ls-executed", nil},
		{"failed", ledger.ListSchedulesInput{Status: ledger.ScheduleFailed, Limit: 10}, "ls-failed", nil},
		{"canceled", ledger.ListSchedulesInput{Status: ledger.ScheduleCanceled, Limit: 10}, "ls-canceled", nil},
		{"status and cursor", ledger.ListSchedulesInput{Status: ledger.ScheduleScheduled, Before: waiting2.ID, Limit: 10}, "ls-waiting-1", nil},
		{"max cursor", ledger.ListSchedulesInput{Before: uuid.Max, Limit: 10}, all, nil},
		{"cursor below everything", ledger.ListSchedulesInput{Before: uuid.MustParse("00000000-0000-7000-8000-000000000001"), Limit: 10}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListSchedules(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListSchedules() error = %v, want %v", err, tt.err)
			}
			if err == nil && ids(got) != tt.want {
				t.Fatalf("schedules = %s, want %s", ids(got), tt.want)
			}
		})
	}
	_, err := e.m.Scheduled(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)
}
