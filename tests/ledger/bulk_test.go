package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestBulkRequest(t *testing.T) {
	t.Parallel()
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, MaxBatch: 100})
	ctx := context.Background()
	a := e.funded(t, 500)
	b := e.account(t, "USD", ledger.Debit)

	items := make([]ledger.PostInput, 600)
	for i := range items {
		items[i] = transfer("", a.ID, b.ID, 1)
	}
	bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "import-1", Transactions: items})
	if err != nil || bulk.Status != ledger.BulkPending || bulk.Total != 600 || bulk.Processed != 0 {
		t.Fatalf("created = %+v, %v", bulk, err)
	}
	if found, err := e.m.ProcessBulk(ctx); err != nil || !found {
		t.Fatalf("process = %v, %v", found, err)
	}
	bulk, err = e.m.Bulk(ctx, bulk.ID)
	if err != nil || bulk.Status != ledger.BulkCompleted || bulk.Succeeded != 500 || bulk.Failed != 100 || bulk.CompletedAt == nil {
		t.Fatalf("after processing = %+v, %v", bulk, err)
	}
	if found, _ := e.m.ProcessBulk(ctx); found {
		t.Fatal("a completed bulk request was claimed again")
	}
	if got := e.get(t, b.ID).Posted.Amount; got != amt(500) {
		t.Fatalf("b = %s, want 500", got)
	}

	t.Run("results page in order with outcomes", func(t *testing.T) {
		var all []ledger.BulkResult
		after := -1
		for {
			page, err := e.m.BulkResults(ctx, bulk.ID, after, 250)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, page...)
			if len(page) < 250 {
				break
			}
			after = page[len(page)-1].Index
		}
		if len(all) != 600 || all[0].TransactionID == nil || all[499].TransactionID == nil || all[500].ErrorCode == nil ||
			*all[500].ErrorCode != "insufficient_funds" || all[599].Index != 599 {
			t.Fatalf("results = %d, first %+v, 500th %+v", len(all), all[0], all[500])
		}
		txn, err := e.m.Transaction(ctx, *all[42].TransactionID)
		if err != nil || txn.IdempotencyKey != "import-1/42" {
			t.Fatalf("item 42 transaction = %+v, %v", txn, err)
		}
	})

	t.Run("submission is idempotent", func(t *testing.T) {
		again, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "import-1", Transactions: items})
		if err != nil || again.ID != bulk.ID || again.Status != ledger.BulkCompleted {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		_, err = e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "import-1", Transactions: items[:10]})
		wantErr(t, err, ledger.ErrIdempotencyConflict)
	})

	t.Run("validation", func(t *testing.T) {
		for name, in := range map[string]ledger.CreateBulkInput{
			"empty":    {IdempotencyKey: "v1"},
			"too many": {IdempotencyKey: "v2", Transactions: make([]ledger.PostInput, 10_001)},
			"bad item": {IdempotencyKey: "v3", Transactions: []ledger.PostInput{transfer("", a.ID, b.ID, 1), {}}},
			"no key":   {Transactions: []ledger.PostInput{transfer("", a.ID, b.ID, 1)}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := e.m.CreateBulk(ctx, in)
				wantErr(t, err, ledger.ErrInvalid)
			})
		}
	})

	if got := countTypes(e.published(t, "bulk_request.completed"))["bulk_request.completed"]; got != 1 {
		t.Fatalf("bulk_request.completed events = %d", got)
	}
	e.verify(t)
}

func TestBulkResumesAfterCrash(t *testing.T) {
	t.Parallel()
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, MaxBatch: 4})
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	items := make([]ledger.PostInput, 10)
	for i := range items {
		items[i] = transfer("", a.ID, b.ID, 1)
	}
	bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "crash", Transactions: items})
	if err != nil {
		t.Fatal(err)
	}

	early := make([]ledger.PostInput, 3)
	for i := range early {
		early[i] = transfer(fmt.Sprintf("crash/%d", i), a.ID, b.ID, 1)
	}
	results, err := e.m.PostBatch(ctx, early, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `
		UPDATE ledger_bulk_requests SET status = 'processing', lease_until = now() - interval '1 second', started_at = now()
		WHERE id = $1`, bulk.ID); err != nil {
		t.Fatal(err)
	}

	if found, err := e.m.ProcessBulk(ctx); err != nil || !found {
		t.Fatalf("takeover = %v, %v", found, err)
	}
	done, _ := e.m.Bulk(ctx, bulk.ID)
	if done.Status != ledger.BulkCompleted || done.Succeeded != 10 {
		t.Fatalf("after takeover = %+v", done)
	}
	if got := e.get(t, b.ID).Posted.Amount; got != amt(10) {
		t.Fatalf("b = %s, want exactly 10 postings of 1", got)
	}
	items2, err := e.m.BulkResults(ctx, bulk.ID, -1, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range items2 {
		if *r.TransactionID != results[i].Transaction.ID {
			t.Fatalf("item %d = %s, want the transaction posted before the crash %s", i, *r.TransactionID, results[i].Transaction.ID)
		}
	}
	e.verify(t)
}

func TestConcurrentBulkWorkers(t *testing.T) {
	t.Parallel()
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, MaxBatch: 10})
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	for n := range 4 {
		items := make([]ledger.PostInput, 25)
		for i := range items {
			items[i] = transfer("", a.ID, b.ID, 1)
		}
		if _, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: fmt.Sprint("job-", n), Transactions: items}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for {
				found, err := e.m.ProcessBulk(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if !found {
					return
				}
			}
		})
	}
	wg.Wait()
	if got := e.get(t, b.ID).Posted.Amount; got != amt(100) {
		t.Fatalf("b = %s, want 100", got)
	}
	e.verify(t)
}

func TestHTTPBulk(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := a.account("cash", "debit", "")
	var items []string
	for range 3 {
		items = append(items, transferJSON(equity, cash, "5"))
	}
	items = append(items, transferJSON(cash, equity, "1000"))
	body := `{"transactions":[` + strings.Join(items, ",") + `]}`

	bulk := a.must(http.StatusAccepted, http.MethodPost, "/v1/bulk_requests", "bulk-1", body)
	id := requirePrefix(t, bulk["id"], "blk")
	if bulk["object"] != "bulk_request" || bulk["status"] != "pending" || bulk["total"] != float64(4) {
		t.Fatalf("bulk = %v", bulk)
	}
	if found, err := a.e.m.ProcessBulk(context.Background()); err != nil || !found {
		t.Fatalf("process = %v, %v", found, err)
	}
	got := a.must(http.StatusOK, http.MethodGet, "/v1/bulk_requests/"+id, "", "")
	if got["status"] != "completed" || got["succeeded"] != float64(3) || got["failed"] != float64(1) {
		t.Fatalf("bulk = %v", got)
	}
	results := a.list("/v1/bulk_requests/" + id + "/results?limit=2")
	if len(results) != 4 || results[0]["status"] != "succeeded" || results[3]["status"] != "failed" ||
		results[3]["error"].(map[string]any)["code"] != "insufficient_funds" {
		t.Fatalf("results = %v", results)
	}
	requirePrefix(t, results[0]["transaction_id"], "txn")

	big := make([]string, 7_000)
	for i := range big {
		big[i] = transferJSON(equity, cash, "1")
	}
	bigBody := `{"transactions":[` + strings.Join(big, ",") + `]}`
	if len(bigBody) <= 1<<20 {
		t.Fatalf("test body is only %d bytes", len(bigBody))
	}
	if got := a.must(http.StatusAccepted, http.MethodPost, "/v1/bulk_requests", "bulk-big", bigBody); got["total"] != float64(7_000) {
		t.Fatalf("big bulk = %v", got)
	}

	a.must(http.StatusBadRequest, http.MethodPost, "/v1/bulk_requests", "", body)
	a.must(http.StatusUnprocessableEntity, http.MethodPost, "/v1/bulk_requests", "bulk-2", `{"transactions":[]}`)
	a.must(http.StatusNotFound, http.MethodGet, "/v1/bulk_requests/blk_01h455vb4pex5vsknk084sn02q", "", "")
}
