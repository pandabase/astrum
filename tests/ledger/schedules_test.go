package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func scheduleInput(key string, from, to uuid.UUID, amount int64, at time.Time) ledger.ScheduleInput {
	return ledger.ScheduleInput{PostInput: transfer(key, from, to, amount), ExecuteAt: at}
}

func TestScheduleExecutesExactlyOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	due, err := e.m.Schedule(ctx, scheduleInput("rent-jan", a.ID, b.ID, 300, time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	future, err := e.m.Schedule(ctx, scheduleInput("rent-feb", a.ID, b.ID, 300, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if _, _, err := e.m.ExecuteDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	got, err := e.m.Scheduled(ctx, due.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ledger.ScheduleExecuted || got.TransactionID == nil {
		t.Fatalf("due schedule = %+v", got)
	}
	txn, err := e.m.Transaction(ctx, *got.TransactionID)
	if err != nil || txn.IdempotencyKey != "rent-jan" {
		t.Fatalf("executed transaction = %+v, %v", txn, err)
	}
	if st, _ := e.m.Scheduled(ctx, future.ID); st.Status != ledger.ScheduleScheduled {
		t.Fatalf("future schedule status = %s", st.Status)
	}
	if bal := e.balance(t, b.ID); bal != 300 {
		t.Fatalf("b = %d, want 300 (executed once)", bal)
	}
	e.verify(t)
}

func TestScheduleFailureIsRecorded(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	ok, _ := e.m.Schedule(ctx, scheduleInput("ok", a.ID, b.ID, 60, time.Now().Add(-2*time.Second)))
	broke, _ := e.m.Schedule(ctx, scheduleInput("broke", a.ID, b.ID, 60, time.Now().Add(-time.Second)))

	executed, failed, err := e.m.ExecuteDue(ctx)
	if err != nil || executed != 1 || failed != 1 {
		t.Fatalf("ExecuteDue() = %d executed, %d failed, %v", executed, failed, err)
	}
	if st, _ := e.m.Scheduled(ctx, ok.ID); st.Status != ledger.ScheduleExecuted {
		t.Fatalf("ok status = %s", st.Status)
	}
	st, _ := e.m.Scheduled(ctx, broke.ID)
	if st.Status != ledger.ScheduleFailed || st.Failure == nil {
		t.Fatalf("broke = %+v", st)
	}
	e.verify(t)
}

func TestScheduleCancelAndReplay(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	at := time.Now().Add(time.Hour)

	in := scheduleInput("payroll", a.ID, b.ID, 10, at)
	st, err := e.m.Schedule(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.m.Schedule(ctx, in)
	if err != nil || again.ID != st.ID {
		t.Fatalf("schedule replay = %s, %v", again.ID, err)
	}
	moved := scheduleInput("payroll", a.ID, b.ID, 10, at.Add(time.Minute))
	_, err = e.m.Schedule(ctx, moved)
	wantErr(t, err, ledger.ErrIdempotencyConflict)

	for range 2 {
		canceled, err := e.m.CancelSchedule(ctx, st.ID)
		if err != nil || canceled.Status != ledger.ScheduleCanceled {
			t.Fatalf("cancel = %+v, %v", canceled, err)
		}
	}

	executedLater, _ := e.m.Schedule(ctx, scheduleInput("done", a.ID, b.ID, 10, time.Now().Add(-time.Second)))
	if _, _, err := e.m.ExecuteDue(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = e.m.CancelSchedule(ctx, executedLater.ID)
	wantErr(t, err, ledger.ErrScheduleNotPending)

	_, err = e.m.Schedule(ctx, scheduleInput("ghost", a.ID, uuid.New(), 1, at))
	wantErr(t, err, ledger.ErrNotFound)
	_, err = e.m.CancelSchedule(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)
}

func TestConcurrentSchedulers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 10_000)
	b := e.account(t, "USD", ledger.Debit)

	const schedules = 60
	for i := range schedules {
		key := "bulk-" + uuid.NewString()
		if _, err := e.m.Schedule(ctx, scheduleInput(key, a.ID, b.ID, 10, time.Now().Add(-time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			for range 5 {
				if _, _, err := e.m.ExecuteDue(ctx); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()

	if bal := e.balance(t, b.ID); bal != schedules*10 {
		t.Fatalf("b = %d, want %d", bal, schedules*10)
	}
	e.verify(t)
}

func TestListSchedules(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	due, err := e.m.Schedule(ctx, scheduleInput("list-due", a.ID, b.ID, 100, time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	later, err := e.m.Schedule(ctx, scheduleInput("list-later", a.ID, b.ID, 100, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.m.ExecuteDue(ctx); err != nil {
		t.Fatal(err)
	}

	all, err := e.m.ListSchedules(ctx, ledger.ListSchedulesInput{Limit: 10})
	if err != nil || len(all) != 2 || all[0].ID != later.ID || all[1].ID != due.ID {
		t.Fatalf("all = %+v, %v", all, err)
	}
	waiting, err := e.m.ListSchedules(ctx, ledger.ListSchedulesInput{Status: ledger.ScheduleScheduled, Limit: 10})
	if err != nil || len(waiting) != 1 || waiting[0].ID != later.ID {
		t.Fatalf("scheduled = %+v, %v", waiting, err)
	}
	executed, err := e.m.ListSchedules(ctx, ledger.ListSchedulesInput{Status: ledger.ScheduleExecuted, Before: later.ID, Limit: 10})
	if err != nil || len(executed) != 1 || executed[0].ID != due.ID {
		t.Fatalf("executed = %+v, %v", executed, err)
	}
	_, err = e.m.ListSchedules(ctx, ledger.ListSchedulesInput{Status: "pending", Limit: 10})
	wantErr(t, err, ledger.ErrInvalid)
}
