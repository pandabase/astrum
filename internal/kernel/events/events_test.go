package events

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	now := time.Unix(1_800_000_000, 0)
	sig := Sign("whsec_x", now, body)
	if !strings.HasPrefix(sig, "t=1800000000,v1=") {
		t.Fatalf("signature = %s", sig)
	}
	tests := []struct {
		name   string
		secret string
		body   []byte
		at     time.Time
		header string
		want   bool
	}{
		{"valid", "whsec_x", body, now.Add(time.Minute), sig, true},
		{"wrong secret", "whsec_y", body, now, sig, false},
		{"tampered body", "whsec_x", []byte(`{"id":"evt_2"}`), now, sig, false},
		{"too old", "whsec_x", body, now.Add(10 * time.Minute), sig, false},
		{"garbage", "whsec_x", body, now, "t=x,v1=y", false},
		{"missing signature", "whsec_x", body, now, "t=1800000000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(tt.secret, tt.header, tt.body, tt.at, 5*time.Minute); got != tt.want {
				t.Fatalf("Verify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubscribed(t *testing.T) {
	tests := []struct {
		types []string
		event string
		want  bool
	}{
		{nil, "transaction.posted", true},
		{[]string{"transaction.posted"}, "transaction.posted", true},
		{[]string{"transaction.posted"}, "transaction.created", false},
		{[]string{"transaction.*"}, "transaction.created", true},
		{[]string{"transaction.*"}, "transactions.created", false},
		{[]string{"hold.*", "account.created"}, "account.created", true},
		{[]string{"*"}, "balance_monitor.triggered", true},
	}
	for _, tt := range tests {
		if got := subscribed(tt.types, tt.event); got != tt.want {
			t.Errorf("subscribed(%v, %s) = %v, want %v", tt.types, tt.event, got, tt.want)
		}
	}
}

func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"93.184.216.34:443":   true,
		"[2606:4700::1]:443":  true,
		"127.0.0.1:443":       false,
		"10.1.2.3:443":        false,
		"192.168.1.1:80":      false,
		"169.254.169.254:80":  false,
		"[::1]:443":           false,
		"[::ffff:10.0.0.1]:1": false,
		"0.0.0.0:80":          false,
	} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != ok {
			t.Errorf("publicOnly(%s) = %v, want allowed %v", addr, err, ok)
		}
	}
}

type receiver struct {
	mu       sync.Mutex
	status   int
	requests []received
	srv      *httptest.Server
}

type received struct {
	header http.Header
	body   []byte
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{status: http.StatusOK}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.requests = append(r.requests, received{req.Header.Clone(), body})
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) set(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *receiver) got() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.requests...)
}

func setup(t *testing.T, cfg Config) *Service {
	t.Helper()
	cfg.AllowInsecureURLs = true
	s := NewService(nil, testdb.Logger(), cfg)
	s.pool = testdb.New(t, map[string]fs.FS{s.Name(): s.Migrations()})
	return s
}

func publish(t *testing.T, s *Service, evs ...Event) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), s.pool, func(tx pgx.Tx) error { return Insert(context.Background(), tx, evs...) }); err != nil {
		t.Fatal(err)
	}
}

func event(t *testing.T, eventType string, data any) Event {
	t.Helper()
	e, err := New(eventType, data)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func dispatchAll(t *testing.T, s *Service, want int) {
	t.Helper()
	total := 0
	for deadline := time.Now().Add(30 * time.Second); total < want; {
		n, err := s.Dispatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if total += n; total < want {
			if time.Now().After(deadline) {
				t.Fatalf("dispatched %d of %d events", total, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func drain(t *testing.T, s *Service, want int) {
	t.Helper()
	dispatchAll(t, s, want)
	if _, err := s.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverySignedAndFiltered(t *testing.T) {
	s := setup(t, Config{})
	ctx := context.Background()
	rcv := newReceiver(t)
	all, err := s.CreateEndpoint(ctx, EndpointInput{URL: rcv.srv.URL + "/all"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(all.Secret, "whsec_") {
		t.Fatalf("secret = %q", all.Secret)
	}
	holds, err := s.CreateEndpoint(ctx, EndpointInput{URL: rcv.srv.URL + "/holds", EventTypes: []string{"hold.*"}})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.CreateEndpoint(ctx, EndpointInput{URL: rcv.srv.URL + "/off", Enabled: &off}); err != nil {
		t.Fatal(err)
	}

	publish(t, s, event(t, "transaction.created", map[string]string{"object": "transaction"}), event(t, "hold.created", map[string]string{"object": "hold"}))
	drain(t, s, 2)

	got := rcv.got()
	if len(got) != 3 {
		t.Fatalf("received %d requests, want 3", len(got))
	}
	for _, r := range got {
		var body struct {
			Object string `json:"object"`
			ID     string `json:"id"`
			Type   string `json:"type"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil || body.Object != "event" || !strings.HasPrefix(body.ID, "evt_") {
			t.Fatalf("body = %s", r.body)
		}
		if r.header.Get(EventIDHeader) != body.ID {
			t.Fatalf("event id header = %s, body id %s", r.header.Get(EventIDHeader), body.ID)
		}
		if !Verify(all.Secret, r.header.Get(SignatureHeader), r.body, time.Now(), time.Minute) &&
			!Verify(holds.Secret, r.header.Get(SignatureHeader), r.body, time.Now(), time.Minute) {
			t.Fatalf("signature does not verify: %s", r.header.Get(SignatureHeader))
		}
	}

	deliveries, err := s.ListDeliveries(ctx, ListDeliveriesInput{EndpointID: holds.ID, Limit: 10})
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "succeeded" || deliveries[0].EventType != "hold.created" {
		t.Fatalf("holds deliveries = %+v, %v", deliveries, err)
	}

	drain(t, s, 0)
	if n := len(rcv.got()); n != 3 {
		t.Fatalf("received %d requests after a second round", n)
	}
}

func TestRetriesThenFails(t *testing.T) {
	s := setup(t, Config{Retries: []time.Duration{time.Millisecond, time.Millisecond}})
	ctx := context.Background()
	rcv := newReceiver(t)
	rcv.set(http.StatusServiceUnavailable)
	ep, err := s.CreateEndpoint(ctx, EndpointInput{URL: rcv.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, s, event(t, "account.created", map[string]string{"object": "account"}))

	dispatchAll(t, s, 1)
	var d Delivery
	for deadline := time.Now().Add(10 * time.Second); d.Status != "failed"; {
		if _, err := s.Deliver(ctx); err != nil {
			t.Fatal(err)
		}
		ds, err := s.ListDeliveries(ctx, ListDeliveriesInput{EndpointID: ep.ID, Limit: 10})
		if err != nil || len(ds) != 1 {
			t.Fatalf("deliveries = %+v, %v", ds, err)
		}
		if d = ds[0]; time.Now().After(deadline) {
			t.Fatalf("delivery never failed: %+v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.Status != "failed" || d.Attempts != 3 || *d.LastStatusCode != 503 || d.NextAttemptAt != nil {
		t.Fatalf("after exhausting retries = %+v", d)
	}

	rcv.set(http.StatusNoContent)
	if d, err = s.RetryDelivery(ctx, d.ID); err != nil || d.Status != "pending" {
		t.Fatalf("retry = %+v, %v", d, err)
	}
	drain(t, s, 0)
	if d, _ = s.Delivery(ctx, d.ID); d.Status != "succeeded" || d.Attempts != 4 || d.DeliveredAt == nil {
		t.Fatalf("after manual retry = %+v", d)
	}
	if d, _ = s.RetryDelivery(ctx, d.ID); d.Status != "succeeded" {
		t.Fatalf("retrying a delivered event changed it: %+v", d)
	}
}

func TestLeaseRecoversCrashedSender(t *testing.T) {
	s := setup(t, Config{Lease: 50 * time.Millisecond, Timeout: 10 * time.Millisecond})
	ctx := context.Background()
	rcv := newReceiver(t)
	if _, err := s.CreateEndpoint(ctx, EndpointInput{URL: rcv.srv.URL}); err != nil {
		t.Fatal(err)
	}
	publish(t, s, event(t, "hold.voided", map[string]string{"object": "hold"}))
	dispatchAll(t, s, 1)
	if claimed, err := s.claim(ctx); err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	if n, _ := s.Deliver(ctx); n != 0 {
		t.Fatalf("leased delivery was sent again before the lease ended")
	}
	time.Sleep(100 * time.Millisecond)
	if n, _ := s.Deliver(ctx); n != 1 || len(rcv.got()) != 1 {
		t.Fatalf("after the lease: sent %d, received %d", n, len(rcv.got()))
	}
}

func TestDispatchWaitsForInFlight(t *testing.T) {
	s := setup(t, Config{})
	ctx := context.Background()
	rcv := newReceiver(t)
	if _, err := s.CreateEndpoint(ctx, EndpointInput{URL: rcv.srv.URL}); err != nil {
		t.Fatal(err)
	}

	slow, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback(ctx)
	if err := Insert(ctx, slow, event(t, "transaction.created", map[string]string{"n": "older"})); err != nil {
		t.Fatal(err)
	}
	publish(t, s, event(t, "transaction.created", map[string]string{"n": "newer"}))

	if n, err := s.Dispatch(ctx); err != nil || n != 0 {
		t.Fatalf("dispatched %d while an older event was in flight (%v)", n, err)
	}
	if err := slow.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	dispatchAll(t, s, 2)
	if _, err := s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rcv.got(); len(got) != 2 {
		t.Fatalf("received %d", len(got))
	}
}

func TestEndpointValidation(t *testing.T) {
	strict := NewService(nil, testdb.Logger(), Config{})
	for _, tt := range []struct {
		url   string
		types []string
		ok    bool
	}{
		{"https://example.com/hooks", nil, true},
		{"https://example.com/hooks", []string{"transaction.*", "hold.created", "*"}, true},
		{"http://example.com/hooks", nil, false},
		{"ftp://example.com", nil, false},
		{"https://user:pass@example.com", nil, false},
		{"/relative", nil, false},
		{"https://example.com", []string{"Transaction.Created"}, false},
		{"https://example.com", []string{"transaction.*.x"}, false},
	} {
		err := strict.validateEndpoint(tt.url, "", tt.types)
		if (err == nil) != tt.ok {
			t.Errorf("validateEndpoint(%s, %v) = %v, want ok %v", tt.url, tt.types, err, tt.ok)
		}
	}
	lax := NewService(nil, testdb.Logger(), Config{AllowInsecureURLs: true})
	if err := lax.validateEndpoint("http://localhost:9000", "", nil); err != nil {
		t.Fatalf("insecure URL refused with AllowInsecureURLs: %v", err)
	}
}

func TestHTTP(t *testing.T) {
	s := setup(t, Config{})
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), mux))
	t.Cleanup(srv.Close)
	rcv := newReceiver(t)

	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != want {
			t.Fatalf("%s %s = %d %v, want %d", method, path, resp.StatusCode, out, want)
		}
		return out
	}

	ep := call(http.MethodPost, "/v1/webhook_endpoints", fmt.Sprintf(`{"url":%q,"event_types":["hold.*"]}`, rcv.srv.URL), 201)
	id := ep["id"].(string)
	if !strings.HasPrefix(id, "we_") || !strings.HasPrefix(ep["secret"].(string), "whsec_") {
		t.Fatalf("endpoint = %v", ep)
	}
	if got := call(http.MethodGet, "/v1/webhook_endpoints/"+id, "", 200); got["secret"] != nil {
		t.Fatalf("secret leaked on read: %v", got)
	}
	if got := call(http.MethodPatch, "/v1/webhook_endpoints/"+id, `{"enabled":false}`, 200); got["enabled"] != false || got["version"] != float64(1) {
		t.Fatalf("patched = %v", got)
	}
	call(http.MethodPatch, "/v1/webhook_endpoints/"+id, `{"enabled":true}`, 200)

	publish(t, s, event(t, "hold.created", map[string]string{"object": "hold"}))
	drain(t, s, 1)
	evs := call(http.MethodGet, "/v1/events?type=hold.created", "", 200)["data"].([]any)
	if len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}
	evID := evs[0].(map[string]any)["id"].(string)
	call(http.MethodGet, "/v1/events/"+evID, "", 200)
	ds := call(http.MethodGet, "/v1/webhook_deliveries?event_id="+evID, "", 200)["data"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("deliveries = %v", ds)
	}
	dID := ds[0].(map[string]any)["id"].(string)
	call(http.MethodPost, "/v1/webhook_deliveries/"+dID+"/retry", "", 200)

	call(http.MethodPost, "/v1/webhook_endpoints", `{"url":"ftp://x"}`, 422)
	call(http.MethodGet, "/v1/events/"+id, "", 400)
	call(http.MethodGet, "/v1/webhook_deliveries?status=lost", "", 422)
	if got := call(http.MethodDelete, "/v1/webhook_endpoints/"+id, "", 200); got["deleted"] != true {
		t.Fatalf("delete = %v", got)
	}
	call(http.MethodGet, "/v1/webhook_endpoints/"+id, "", 404)
}

func idAtRandom(t time.Time) uuid.UUID {
	id := idAt(t)
	_, _ = rand.Read(id[6:])
	id[6] = 0x70 | id[6]&0x0f
	id[8] = 0x80 | id[8]&0x3f
	return id
}

func TestPrune(t *testing.T) {
	s := setup(t, Config{Retention: 24 * time.Hour, PruneBatch: 2, Retries: []time.Duration{time.Hour}})
	ctx := context.Background()
	ok, failing := newReceiver(t), newReceiver(t)
	failing.set(http.StatusInternalServerError)
	if _, err := s.CreateEndpoint(ctx, EndpointInput{URL: ok.srv.URL, EventTypes: []string{"hold.*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEndpoint(ctx, EndpointInput{URL: failing.srv.URL, EventTypes: []string{"transaction.*"}}); err != nil {
		t.Fatal(err)
	}
	old := func(eventType string) Event {
		e := event(t, eventType, map[string]string{"object": "x"})
		e.ID = idAtRandom(time.Now().Add(-40 * 24 * time.Hour))
		return e
	}

	delivered := []Event{old("hold.created"), old("hold.voided"), old("hold.expired")}
	stuck := old("transaction.created")
	recent := event(t, "hold.created", map[string]string{"object": "x"})
	publish(t, s, append(delivered, stuck, recent)...)
	drain(t, s, 5)
	undispatched := old("hold.created")
	publish(t, s, undispatched)

	n, err := s.Prune(ctx)
	if err != nil || n != 3 {
		t.Fatalf("Prune = %d, %v; want the 3 old delivered events", n, err)
	}
	for _, e := range delivered {
		if _, err := s.Event(ctx, e.ID); err != ErrNotFound {
			t.Fatalf("event %s survived: %v", e.ID, err)
		}
		if ds, _ := s.ListDeliveries(ctx, ListDeliveriesInput{EventID: e.ID, Limit: 10}); len(ds) != 0 {
			t.Fatalf("deliveries of pruned event %s survived: %+v", e.ID, ds)
		}
	}
	for name, e := range map[string]Event{"pending delivery": stuck, "recent": recent, "not dispatched": undispatched} {
		if _, err := s.Event(ctx, e.ID); err != nil {
			t.Fatalf("%s event was pruned: %v", name, err)
		}
	}
	if n, err := s.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("second Prune = %d, %v", n, err)
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM events`)
		return err
	})
	if db.Code(err) != "23001" {
		t.Fatalf("raw delete = %v, want restrict_violation", err)
	}
}
