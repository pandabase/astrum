package events_test

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

type edgeEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *events.Service
	srv  *httptest.Server
}

func newEdgeEnv(t *testing.T, cfg events.Config) *edgeEnv {
	t.Helper()
	pool := testdb.New(t, map[string]fs.FS{"events": events.Migrations()})
	return edgeEnvOn(t, pool, cfg)
}

func edgeEnvOn(t *testing.T, pool *pgxpool.Pool, cfg events.Config) *edgeEnv {
	t.Helper()
	svc := events.NewService(pool, testdb.Logger(), cfg)
	mux := http.NewServeMux()
	svc.Routes(mux)
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), mux))
	t.Cleanup(srv.Close)
	return &edgeEnv{t: t, pool: pool, svc: svc, srv: srv}
}

type edgeResp struct {
	status int
	raw    []byte
	body   map[string]any
}

func (e *edgeEnv) call(method, path, body string) edgeResp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	out := edgeResp{status: resp.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *edgeEnv) must(want int, method, path, body string) map[string]any {
	e.t.Helper()
	r := e.call(method, path, body)
	if r.status != want {
		e.t.Fatalf("%s %s = %d %s, want %d", method, path, r.status, r.raw, want)
	}
	return r.body
}

func (e *edgeEnv) walk(path string) []map[string]any {
	e.t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var all []map[string]any
	next := path
	for range 100 {
		page := e.must(200, http.MethodGet, next, "")
		for _, item := range page["data"].([]any) {
			all = append(all, item.(map[string]any))
		}
		if page["has_more"] != true {
			if page["next_cursor"] != nil {
				e.t.Fatalf("last page of %s has a cursor", path)
			}
			return all
		}
		next = path + sep + "cursor=" + page["next_cursor"].(string)
	}
	e.t.Fatalf("pagination of %s does not terminate", path)
	return nil
}

func (e *edgeEnv) endpoint(url string, types ...string) events.Endpoint {
	e.t.Helper()
	ep, err := e.svc.CreateEndpoint(context.Background(), events.EndpointInput{URL: url, EventTypes: types})
	if err != nil {
		e.t.Fatal(err)
	}
	return ep
}

func (e *edgeEnv) publish(evs ...events.Event) {
	e.t.Helper()
	if err := events.Insert(context.Background(), e.pool, evs...); err != nil {
		e.t.Fatal(err)
	}
}

func edgeEvent(t *testing.T, eventType string) events.Event {
	t.Helper()
	ev, err := events.New(eventType, map[string]string{"object": strings.SplitN(eventType, ".", 2)[0]})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func edgeOldID(t *testing.T, at time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(at.UnixMilli()))
	copy(id[:6], ms[2:])
	id[6] = 0x70 | id[6]&0x0f
	id[8] = 0x80 | id[8]&0x3f
	return id
}

func (e *edgeEnv) dispatch(want int) {
	e.t.Helper()
	total := 0
	deadline := time.Now().Add(30 * time.Second)
	for total < want {
		n, err := e.svc.Dispatch(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		total += n
		if total >= want {
			break
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("dispatched %d of %d events", total, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *edgeEnv) deliver() int {
	e.t.Helper()
	n, err := e.svc.Deliver(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *edgeEnv) deliveries(endpoint uuid.UUID) []events.Delivery {
	e.t.Helper()
	ds, err := e.svc.ListDeliveries(context.Background(), events.ListDeliveriesInput{EndpointID: endpoint, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return ds
}

func (e *edgeEnv) one(endpoint uuid.UUID) events.Delivery {
	e.t.Helper()
	ds := e.deliveries(endpoint)
	if len(ds) != 1 {
		e.t.Fatalf("deliveries for %s = %d, want 1", endpoint, len(ds))
	}
	return ds[0]
}

func (e *edgeEnv) makeDue() {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE webhook_deliveries SET next_attempt_at = now() WHERE status = 'pending'`); err != nil {
		e.t.Fatal(err)
	}
}

type edgeHit struct {
	path   string
	header http.Header
	body   []byte
}

type edgeReceiver struct {
	mu     sync.Mutex
	hits   []edgeHit
	status map[string]int
	srv    *httptest.Server
}

func newEdgeReceiver(t *testing.T, tls bool) *edgeReceiver {
	t.Helper()
	r := &edgeReceiver{status: map[string]int{}}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits = append(r.hits, edgeHit{req.URL.Path, req.Header.Clone(), body})
		status, ok := r.status[req.URL.Path]
		r.mu.Unlock()
		if !ok {
			status = http.StatusOK
		}
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
	})
	if tls {
		r.srv = httptest.NewTLSServer(h)
	} else {
		r.srv = httptest.NewServer(h)
	}
	t.Cleanup(r.srv.Close)
	return r
}

func (r *edgeReceiver) set(path string, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status[path] = status
}

func (r *edgeReceiver) got(path string) []edgeHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []edgeHit
	for _, h := range r.hits {
		if path == "" || h.path == path {
			out = append(out, h)
		}
	}
	return out
}

func TestEventsEdgeCreateEndpointValidation(t *testing.T) {
	t.Parallel()
	strict := newEdgeEnv(t, events.Config{})
	lax := edgeEnvOn(t, strict.pool, events.Config{AllowInsecureURLs: true})
	long := func(n int) string {
		base := "https://example.com/"
		return base + strings.Repeat("a", n-len(base))
	}
	types := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Quote("t.x" + strings.Repeat("a", i))
		}
		return "[" + strings.Join(out, ",") + "]"
	}

	tests := []struct {
		name   string
		env    *edgeEnv
		body   string
		status int
		code   string
	}{
		{"https", strict, `{"url":"https://example.com/hooks"}`, 201, ""},
		{"https with port query and fragment", strict, `{"url":"https://example.com:8443/h?x=1#f"}`, 201, ""},
		{"loopback ip", strict, `{"url":"https://127.0.0.1/h"}`, 422, "validation_error"},
		{"ipv6 loopback", strict, `{"url":"https://[::1]/h"}`, 422, "validation_error"},
		{"ipv4 mapped loopback", strict, `{"url":"https://[::ffff:127.0.0.1]/h"}`, 422, "validation_error"},
		{"link local metadata", strict, `{"url":"https://169.254.169.254/latest"}`, 422, "validation_error"},
		{"private", strict, `{"url":"https://10.0.0.1/h"}`, 422, "validation_error"},
		{"unspecified", strict, `{"url":"https://0.0.0.0/h"}`, 422, "validation_error"},
		{"localhost name", strict, `{"url":"https://localhost/h"}`, 422, "validation_error"},
		{"localhost subdomain with trailing dot", strict, `{"url":"https://api.LOCALHOST./h"}`, 422, "validation_error"},
		{"public ip", strict, `{"url":"https://93.184.215.14/h"}`, 201, ""},
		{"url at length limit", strict, fmt.Sprintf(`{"url":%q}`, long(2048)), 201, ""},
		{"url over length limit", strict, fmt.Sprintf(`{"url":%q}`, long(2049)), 422, "validation_error"},
		{"http rejected", strict, `{"url":"http://example.com"}`, 422, "validation_error"},
		{"http loopback rejected", strict, `{"url":"http://127.0.0.1:9"}`, 422, "validation_error"},
		{"uppercase scheme", strict, `{"url":"HTTPS://example.com"}`, 422, "validation_error"},
		{"ftp", strict, `{"url":"ftp://example.com"}`, 422, "validation_error"},
		{"javascript", strict, `{"url":"javascript:alert(1)"}`, 422, "validation_error"},
		{"credentials", strict, `{"url":"https://user:pass@example.com"}`, 422, "validation_error"},
		{"user only", strict, `{"url":"https://user@example.com"}`, 422, "validation_error"},
		{"empty password", strict, `{"url":"https://:@example.com"}`, 422, "validation_error"},
		{"scheme relative", strict, `{"url":"//example.com"}`, 422, "validation_error"},
		{"no host", strict, `{"url":"https:///path"}`, 422, "validation_error"},
		{"bare scheme", strict, `{"url":"https://"}`, 422, "validation_error"},
		{"out of range port", strict, `{"url":"https://example.com:99999999999"}`, 422, "validation_error"},
		{"port zero", strict, `{"url":"https://example.com:0/h"}`, 422, "validation_error"},
		{"highest port", strict, `{"url":"https://example.com:65535/h"}`, 201, ""},
		{"space in host", strict, `{"url":"https://exa mple.com"}`, 422, "validation_error"},
		{"control character", strict, `{"url":"https://example.com/\n"}`, 422, "validation_error"},
		{"empty url", strict, `{"url":""}`, 422, "validation_error"},
		{"missing url", strict, `{}`, 422, "validation_error"},
		{"null body", strict, `null`, 422, "validation_error"},
		{"description at limit", strict, `{"url":"https://example.com","description":"` + strings.Repeat("d", 1024) + `"}`, 201, ""},
		{"description over limit", strict, `{"url":"https://example.com","description":"` + strings.Repeat("d", 1025) + `"}`, 422, "validation_error"},
		{"64 event types", strict, `{"url":"https://example.com","event_types":` + types(64) + `}`, 201, ""},
		{"65 event types", strict, `{"url":"https://example.com","event_types":` + types(65) + `}`, 422, "validation_error"},
		{"null event types", strict, `{"url":"https://example.com","event_types":null}`, 201, ""},
		{"duplicate event types", strict, `{"url":"https://example.com","event_types":["a.b","a.b"]}`, 201, ""},
		{"disabled", strict, `{"url":"https://example.com","enabled":false}`, 201, ""},
		{"unknown member", strict, `{"url":"https://example.com","secret":"whsec_x"}`, 400, httpx.CodeInvalidRequest},
		{"wrong case member", strict, `{"URL":"https://example.com"}`, 400, httpx.CodeInvalidRequest},
		{"duplicate member", strict, `{"url":"https://example.com","url":"https://example.org"}`, 400, httpx.CodeInvalidRequest},
		{"event types not an array", strict, `{"url":"https://example.com","event_types":"a.b"}`, 400, httpx.CodeInvalidRequest},
		{"empty body", strict, ``, 400, httpx.CodeInvalidRequest},
		{"trailing data", strict, `{"url":"https://example.com"}x`, 400, httpx.CodeInvalidRequest},
		{"insecure allowed", lax, `{"url":"http://localhost:9000/h"}`, 201, ""},
		{"insecure still rejects ftp", lax, `{"url":"ftp://localhost"}`, 422, "validation_error"},
		{"insecure still rejects credentials", lax, `{"url":"http://u:p@localhost"}`, 422, "validation_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.env.call(http.MethodPost, "/v1/webhook_endpoints", tt.body)
			if r.status != tt.status {
				t.Fatalf("status = %d %s, want %d", r.status, r.raw, tt.status)
			}
			if tt.status != 201 {
				if r.body["code"] != tt.code {
					t.Fatalf("code = %v, want %s", r.body["code"], tt.code)
				}
				return
			}
			if !strings.HasPrefix(r.body["id"].(string), "we_") || !strings.HasPrefix(r.body["secret"].(string), "whsec_") || r.body["version"] != float64(0) {
				t.Fatalf("created = %s", r.raw)
			}
			if _, ok := r.body["event_types"].([]any); !ok {
				t.Fatalf("event_types = %v, want an array", r.body["event_types"])
			}
		})
	}

	patterns := map[string]bool{
		"*": true, "transaction": true, "transaction.*": true, "transaction.posted": true, "balance_monitor.triggered": true, "a.b.c.*": true,
		"": false, "*.*": false, "transaction.": false, ".posted": false, "a..b": false, "a.*.b": false, "a.**": false,
		"Transaction.posted": false, "a-b.c": false, "a.b1": false, " a.b": false, "a.b ": false, "tránsaction.x": false, "a.*b": false,
	}
	for pattern, ok := range patterns {
		t.Run("event type "+strconv.Quote(pattern), func(t *testing.T) {
			r := strict.call(http.MethodPost, "/v1/webhook_endpoints", fmt.Sprintf(`{"url":"https://example.com","event_types":[%q]}`, pattern))
			if (r.status == 201) != ok {
				t.Fatalf("status = %d %s, want accepted %v", r.status, r.raw, ok)
			}
			if !ok && (r.status != 422 || !strings.Contains(r.body["detail"].(string), strconv.Quote(pattern))) {
				t.Fatalf("rejection = %d %s", r.status, r.raw)
			}
		})
	}
}

func TestEventsEdgeEndpointLifecycleHTTP(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{})
	var ids []string
	for i := range 5 {
		created := e.must(201, http.MethodPost, "/v1/webhook_endpoints", fmt.Sprintf(`{"url":"https://example.com/%d","description":"n%d"}`, i, i))
		ids = append(ids, created["id"].(string))
	}

	t.Run("list pages newest first without secrets", func(t *testing.T) {
		all := e.walk("/v1/webhook_endpoints?limit=2")
		if len(all) != 5 {
			t.Fatalf("listed %d endpoints", len(all))
		}
		for i, ep := range all {
			if ep["id"] != ids[len(ids)-1-i] || ep["secret"] != nil || ep["object"] != "webhook_endpoint" {
				t.Fatalf("endpoint %d = %v", i, ep)
			}
		}
		if page := e.must(200, http.MethodGet, "/v1/webhook_endpoints?limit=5", ""); page["has_more"] != false || len(page["data"].([]any)) != 5 {
			t.Fatalf("exact page = %v", page)
		}
		if page := e.must(200, http.MethodGet, "/v1/webhook_endpoints", ""); len(page["data"].([]any)) != 5 {
			t.Fatalf("default page = %v", page)
		}
	})

	t.Run("list parameter validation", func(t *testing.T) {
		for _, q := range []string{"limit=0", "limit=101", "limit=abc", "cursor=***", "cursor=AAAA", "cursor=" + ids[0]} {
			r := e.call(http.MethodGet, "/v1/webhook_endpoints?"+q, "")
			if r.status != 400 || r.body["code"] != httpx.CodeInvalidRequest {
				t.Fatalf("?%s = %d %s", q, r.status, r.raw)
			}
		}
	})

	id := ids[0]
	t.Run("patch", func(t *testing.T) {
		tests := []struct {
			name, body string
			status     int
			version    float64
			check      func(map[string]any) bool
		}{
			{"empty object bumps version", `{}`, 200, 1, func(m map[string]any) bool { return m["url"] == "https://example.com/0" }},
			{"null url keeps url", `{"url":null}`, 200, 2, func(m map[string]any) bool { return m["url"] == "https://example.com/0" }},
			{"change url", `{"url":"https://example.org/new"}`, 200, 3, func(m map[string]any) bool { return m["url"] == "https://example.org/new" }},
			{"clear event types", `{"event_types":[]}`, 200, 4, func(m map[string]any) bool { return len(m["event_types"].([]any)) == 0 }},
			{"set event types", `{"event_types":["hold.*"]}`, 200, 5, func(m map[string]any) bool { return m["event_types"].([]any)[0] == "hold.*" }},
			{"clear description", `{"description":""}`, 200, 6, func(m map[string]any) bool { return m["description"] == "" }},
			{"disable", `{"enabled":false}`, 200, 7, func(m map[string]any) bool { return m["enabled"] == false }},
			{"invalid url", `{"url":"http://example.org"}`, 422, 7, nil},
			{"invalid event type", `{"event_types":["NOPE"]}`, 422, 7, nil},
			{"secret not patchable", `{"secret":"whsec_x"}`, 400, 7, nil},
			{"empty body", ``, 400, 7, nil},
			{"array body", `[]`, 400, 7, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				r := e.call(http.MethodPatch, "/v1/webhook_endpoints/"+id, tt.body)
				if r.status != tt.status {
					t.Fatalf("status = %d %s, want %d", r.status, r.raw, tt.status)
				}
				if tt.check != nil && (!tt.check(r.body) || r.body["secret"] != nil) {
					t.Fatalf("patched = %s", r.raw)
				}
				got := e.must(200, http.MethodGet, "/v1/webhook_endpoints/"+id, "")
				if got["version"] != tt.version {
					t.Fatalf("version = %v, want %v", got["version"], tt.version)
				}
			})
		}
	})

	t.Run("ids", func(t *testing.T) {
		unknown := typeid.Encode("we", uuid.Must(uuid.NewV7()))
		evt := typeid.Encode("evt", uuid.Must(uuid.NewV7()))
		for _, tt := range []struct {
			method, path, body string
			status             int
		}{
			{http.MethodGet, "/v1/webhook_endpoints/" + unknown, "", 404},
			{http.MethodPatch, "/v1/webhook_endpoints/" + unknown, `{}`, 404},
			{http.MethodDelete, "/v1/webhook_endpoints/" + unknown, "", 404},
			{http.MethodGet, "/v1/webhook_endpoints/" + evt, "", 400},
			{http.MethodPatch, "/v1/webhook_endpoints/" + evt, `{}`, 400},
			{http.MethodDelete, "/v1/webhook_endpoints/" + evt, "", 400},
			{http.MethodGet, "/v1/webhook_endpoints/" + strings.ToUpper(id), "", 400},
			{http.MethodGet, "/v1/webhook_endpoints/" + id[:len(id)-1], "", 400},
			{http.MethodGet, "/v1/webhook_endpoints/" + uuid.NewString(), "", 400},
			{http.MethodGet, "/v1/webhook_endpoints/we_%2F..%2Fx", "", 400},
			{http.MethodPut, "/v1/webhook_endpoints/" + id, `{}`, 405},
			{http.MethodPost, "/v1/webhook_endpoints/" + id, `{}`, 405},
			{http.MethodDelete, "/v1/webhook_endpoints", "", 405},
			{http.MethodGet, "/v1/webhook_endpoints/" + id + "/extra", "", 404},
		} {
			r := e.call(tt.method, tt.path, tt.body)
			if r.status != tt.status {
				t.Errorf("%s %s = %d %s, want %d", tt.method, tt.path, r.status, r.raw, tt.status)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		del := e.must(200, http.MethodDelete, "/v1/webhook_endpoints/"+id, "")
		if len(del) != 3 || del["object"] != "webhook_endpoint" || del["id"] != id || del["deleted"] != true {
			t.Fatalf("delete = %v", del)
		}
		e.must(404, http.MethodDelete, "/v1/webhook_endpoints/"+id, "")
		e.must(404, http.MethodGet, "/v1/webhook_endpoints/"+id, "")
		e.must(404, http.MethodPatch, "/v1/webhook_endpoints/"+id, `{}`)
		if n := len(e.walk("/v1/webhook_endpoints")); n != 4 {
			t.Fatalf("endpoints after delete = %d", n)
		}
	})
}

func TestEventsEdgeEventsAndDeliveriesHTTP(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Retries: []time.Duration{time.Hour}})
	rcv := newEdgeReceiver(t, false)
	rcv.set("/fail", http.StatusInternalServerError)
	ok := e.endpoint(rcv.srv.URL+"/ok", "hold.*")
	bad := e.endpoint(rcv.srv.URL+"/fail", "hold.created")

	var evs []events.Event
	for _, typ := range []string{"hold.created", "hold.voided", "hold.created", "account.created"} {
		evs = append(evs, edgeEvent(t, typ))
	}
	e.publish(evs...)
	e.dispatch(4)
	if n := e.deliver(); n != 5 {
		t.Fatalf("delivered %d, want 5", n)
	}

	t.Run("events", func(t *testing.T) {
		all := e.walk("/v1/events?limit=1")
		if len(all) != 4 || all[0]["id"] != typeid.Encode("evt", evs[3].ID) {
			t.Fatalf("events = %v", all)
		}
		for _, ev := range all {
			if ev["object"] != "event" || ev["data"] == nil || ev["created_at"] == nil {
				t.Fatalf("event = %v", ev)
			}
		}
		if got := e.walk("/v1/events?type=hold.created&limit=1"); len(got) != 2 {
			t.Fatalf("hold.created events = %d", len(got))
		}
		for _, q := range []string{"type=hold.*", "type=HOLD.CREATED", "type=nothing.here", "type=hold"} {
			if got := e.walk("/v1/events?" + q); len(got) != 0 {
				t.Fatalf("?%s matched %d events, want exact type matching", q, len(got))
			}
		}
		got := e.must(200, http.MethodGet, "/v1/events/"+typeid.Encode("evt", evs[0].ID), "")
		if got["type"] != "hold.created" || got["data"].(map[string]any)["object"] != "hold" {
			t.Fatalf("event = %v", got)
		}
		e.must(404, http.MethodGet, "/v1/events/"+typeid.Encode("evt", uuid.Must(uuid.NewV7())), "")
		e.must(400, http.MethodGet, "/v1/events/"+typeid.Encode("wd", evs[0].ID), "")
		for _, q := range []string{"type=%00", "type=%FF"} {
			if r := e.call(http.MethodGet, "/v1/events?"+q, ""); r.status >= 500 {
				t.Errorf("?%s = %d %s, want a 4xx or an empty list", q, r.status, r.raw)
			}
		}
		e.must(400, http.MethodGet, "/v1/events?cursor=x", "")
		e.must(400, http.MethodGet, "/v1/events?limit=1000", "")
		e.must(405, http.MethodPost, "/v1/events", "{}")
		e.must(405, http.MethodDelete, "/v1/events/"+typeid.Encode("evt", evs[0].ID), "")
	})

	t.Run("delivery listing and filters", func(t *testing.T) {
		all := e.walk("/v1/webhook_deliveries?limit=2")
		if len(all) != 5 {
			t.Fatalf("deliveries = %d, want 5", len(all))
		}
		for i := 1; i < len(all); i++ {
			if all[i-1]["id"].(string) <= all[i]["id"].(string) {
				t.Fatal("deliveries not newest first")
			}
		}
		okID, badID := typeid.Encode("we", ok.ID), typeid.Encode("we", bad.ID)
		counts := map[string]int{
			"endpoint_id=" + okID:                                  3,
			"endpoint_id=" + badID:                                 2,
			"status=succeeded":                                     3,
			"status=pending":                                       2,
			"status=failed":                                        0,
			"status=pending&endpoint_id=" + okID:                   0,
			"event_id=" + typeid.Encode("evt", evs[0].ID):          2,
			"event_id=" + typeid.Encode("evt", evs[3].ID):          0,
			"event_id=" + typeid.Encode("evt", uuid.New()):         0,
			"endpoint_id=" + typeid.Encode("we", uuid.New()):       0,
			"endpoint_id=" + badID + "&status=pending&limit=1":     2,
			"event_id=" + typeid.Encode("evt", evs[1].ID) + "&x=1": 1,
		}
		for q, want := range counts {
			if got := e.walk("/v1/webhook_deliveries?" + q); len(got) != want {
				t.Errorf("?%s = %d deliveries, want %d", q, len(got), want)
			}
		}
		for q, status := range map[string]int{
			"status=Pending":   422,
			"status=delivered": 422,
			"endpoint_id=" + typeid.Encode("evt", ok.ID): 400,
			"event_id=" + okID:                           400,
			"event_id=" + strings.ToUpper(okID):          400,
			"limit=0":                                    400,
			"cursor=" + okID:                             400,
		} {
			r := e.call(http.MethodGet, "/v1/webhook_deliveries?"+q, "")
			if r.status != status {
				t.Errorf("?%s = %d %s, want %d", q, r.status, r.raw, status)
			}
			if status == 400 && strings.HasPrefix(q, "e") && !strings.HasPrefix(r.body["detail"].(string), strings.SplitN(q, "=", 2)[0]+": ") {
				t.Errorf("?%s detail = %v, want it to name the parameter", q, r.body["detail"])
			}
		}
	})

	t.Run("get and retry delivery", func(t *testing.T) {
		pending := e.deliveries(bad.ID)
		id := typeid.Encode("wd", pending[0].ID)
		got := e.must(200, http.MethodGet, "/v1/webhook_deliveries/"+id, "")
		want := map[string]any{"object": "webhook_delivery", "id": id, "endpoint_id": typeid.Encode("we", bad.ID), "status": "pending", "attempts": float64(1), "last_status_code": float64(500), "last_error": "endpoint responded 500", "delivered_at": nil}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("delivery[%s] = %v, want %v (%v)", k, got[k], v, got)
			}
		}
		if len(got) != 13 || got["next_attempt_at"] == nil || got["last_attempt_at"] == nil {
			t.Fatalf("delivery = %v", got)
		}
		retried := e.must(200, http.MethodPost, "/v1/webhook_deliveries/"+id+"/retry", "")
		if retried["status"] != "pending" || retried["attempts"] != float64(1) {
			t.Fatalf("retry = %v", retried)
		}
		succeeded := typeid.Encode("wd", e.deliveries(ok.ID)[0].ID)
		if r := e.must(200, http.MethodPost, "/v1/webhook_deliveries/"+succeeded+"/retry", ""); r["status"] != "succeeded" || r["delivered_at"] == nil || r["next_attempt_at"] != nil {
			t.Fatalf("retry of a delivered webhook = %v", r)
		}
		unknown := typeid.Encode("wd", uuid.Must(uuid.NewV7()))
		e.must(404, http.MethodGet, "/v1/webhook_deliveries/"+unknown, "")
		e.must(404, http.MethodPost, "/v1/webhook_deliveries/"+unknown+"/retry", "")
		e.must(400, http.MethodGet, "/v1/webhook_deliveries/"+typeid.Encode("we", bad.ID), "")
		e.must(400, http.MethodPost, "/v1/webhook_deliveries/nope/retry", "")
		e.must(405, http.MethodGet, "/v1/webhook_deliveries/"+id+"/retry", "")
		e.must(405, http.MethodDelete, "/v1/webhook_deliveries/"+id, "")
	})

	t.Run("deleting an endpoint removes its deliveries", func(t *testing.T) {
		e.must(200, http.MethodDelete, "/v1/webhook_endpoints/"+typeid.Encode("we", bad.ID), "")
		if got := e.walk("/v1/webhook_deliveries?endpoint_id=" + typeid.Encode("we", bad.ID)); len(got) != 0 {
			t.Fatalf("deliveries of a deleted endpoint = %d", len(got))
		}
		if got := e.walk("/v1/webhook_deliveries"); len(got) != 3 {
			t.Fatalf("remaining deliveries = %d, want 3", len(got))
		}
	})
}

var edgeSignature = regexp.MustCompile(`^t=(\d+),v1=([0-9a-f]{64})$`)

func TestEventsEdgeSignedRequest(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true})
	rcv := newEdgeReceiver(t, false)
	ep := e.endpoint(rcv.srv.URL + "/hook")
	other := e.endpoint(rcv.srv.URL + "/other")
	ev := edgeEvent(t, "transaction.posted")
	e.publish(ev)
	e.dispatch(1)
	before := time.Now().Unix()
	e.deliver()
	after := time.Now().Unix()

	hits := rcv.got("/hook")
	if len(hits) != 1 {
		t.Fatalf("hits = %d", len(hits))
	}
	h := hits[0]
	sig := h.header.Get(events.SignatureHeader)
	m := edgeSignature.FindStringSubmatch(sig)
	if m == nil {
		t.Fatalf("signature header = %q", sig)
	}
	ts, _ := strconv.ParseInt(m[1], 10, 64)
	if ts < before || ts > after {
		t.Fatalf("signature timestamp %d outside [%d, %d]", ts, before, after)
	}
	if events.Sign(ep.Secret, time.Unix(ts, 0), h.body) != sig {
		t.Fatal("signature does not match the body and timestamp")
	}
	if !events.Verify(ep.Secret, sig, h.body, time.Unix(ts, 0), 0) {
		t.Fatal("Verify rejects the exact timestamp with zero tolerance")
	}
	if events.Verify(other.Secret, sig, h.body, time.Unix(ts, 0), time.Minute) {
		t.Fatal("another endpoint's secret verifies the signature")
	}
	if events.Verify(ep.Secret, sig, append([]byte(" "), h.body...), time.Unix(ts, 0), time.Minute) {
		t.Fatal("a modified body verifies")
	}
	wantHeaders := map[string]string{
		"Content-Type":       "application/json",
		"User-Agent":         "Astrum-Webhooks/1",
		events.EventIDHeader: typeid.Encode("evt", ev.ID),
	}
	for k, v := range wantHeaders {
		if got := h.header.Get(k); got != v {
			t.Fatalf("header %s = %q, want %q", k, got, v)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(h.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 5 || body["object"] != "event" || body["id"] != typeid.Encode("evt", ev.ID) || body["type"] != "transaction.posted" ||
		body["data"].(map[string]any)["object"] != "transaction" {
		t.Fatalf("body = %s", h.body)
	}
	otherHits := rcv.got("/other")
	if len(otherHits) != 1 {
		t.Fatalf("second endpoint received %d requests", len(otherHits))
	}
	if !events.Verify(other.Secret, otherHits[0].header.Get(events.SignatureHeader), otherHits[0].body, time.Now(), time.Minute) {
		t.Fatal("second endpoint signature does not verify with its own secret")
	}
	if string(otherHits[0].body) != string(h.body) {
		t.Fatal("endpoints received different bodies for the same event")
	}
}

func TestEventsEdgeVerify(t *testing.T) {
	t.Parallel()
	body := []byte(`{"a":1}`)
	at := time.Unix(1_900_000_000, 0)
	sig := events.Sign("whsec_s", at, body)
	tests := []struct {
		name   string
		header string
		now    time.Time
		tol    time.Duration
		want   bool
	}{
		{"exact", sig, at, 0, true},
		{"at tolerance edge", sig, at.Add(5 * time.Minute), 5 * time.Minute, true},
		{"just past tolerance", sig, at.Add(5*time.Minute + time.Second), 5 * time.Minute, false},
		{"future within tolerance", sig, at.Add(-time.Minute), 5 * time.Minute, true},
		{"future beyond tolerance", sig, at.Add(-time.Hour), 5 * time.Minute, false},
		{"reordered parts", strings.Join([]string{strings.Split(sig, ",")[1], strings.Split(sig, ",")[0]}, ","), at, time.Minute, true},
		{"extra unknown part", sig + ",v0=abc", at, time.Minute, true},
		{"uppercase hex", "t=" + strings.Split(strings.TrimPrefix(sig, "t="), ",")[0] + ",v1=" + strings.ToUpper(strings.Split(sig, "v1=")[1]), at, time.Minute, false},
		{"empty", "", at, time.Minute, false},
		{"negative timestamp", "t=-1,v1=00", time.Unix(-1, 0), time.Minute, false},
		{"spaces", strings.ReplaceAll(sig, ",", ", "), at, time.Minute, false},
		{"duplicate v1 last wins", sig + ",v1=" + strings.Repeat("0", 64), at, time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := events.Verify("whsec_s", tt.header, body, tt.now, tt.tol); got != tt.want {
				t.Fatalf("Verify(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestEventsEdgeResponseStatuses(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Retries: []time.Duration{time.Hour}})
	rcv := newEdgeReceiver(t, false)
	statuses := []int{200, 201, 202, 204, 299, 300, 301, 302, 303, 304, 307, 308, 400, 401, 404, 410, 429, 500, 502, 503}
	eps := map[int]events.Endpoint{}
	for _, s := range statuses {
		path := fmt.Sprint("/s/", s)
		rcv.set(path, s)
		eps[s] = e.endpoint(rcv.srv.URL + path)
	}
	e.publish(edgeEvent(t, "account.created"))
	e.dispatch(1)
	e.deliver()

	for s, ep := range eps {
		t.Run(strconv.Itoa(s), func(t *testing.T) {
			d := e.one(ep.ID)
			if d.Attempts != 1 || d.LastStatusCode == nil || *d.LastStatusCode != s {
				t.Fatalf("delivery = %+v", d)
			}
			if s >= 200 && s <= 299 {
				if d.Status != "succeeded" || d.LastError != nil || d.DeliveredAt == nil || d.NextAttemptAt != nil {
					t.Fatalf("2xx delivery = %+v", d)
				}
				return
			}
			if d.Status != "pending" || d.LastError == nil || *d.LastError != fmt.Sprintf("endpoint responded %d", s) || d.DeliveredAt != nil {
				t.Fatalf("non-2xx delivery = %+v", d)
			}
			if d.NextAttemptAt.Sub(*d.LastAttemptAt) != time.Hour {
				t.Fatalf("backoff = %v, want 1h", d.NextAttemptAt.Sub(*d.LastAttemptAt))
			}
		})
	}
	if hits := rcv.got("/elsewhere"); len(hits) != 0 {
		t.Fatalf("redirects were followed %d times", len(hits))
	}
}

func TestEventsEdgeBackoffSchedule(t *testing.T) {
	t.Parallel()
	retries := []time.Duration{time.Hour, 2 * time.Hour, 90 * time.Minute}
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Retries: retries})
	rcv := newEdgeReceiver(t, false)
	rcv.set("/h", http.StatusServiceUnavailable)
	ep := e.endpoint(rcv.srv.URL + "/h")
	e.publish(edgeEvent(t, "hold.created"))
	e.dispatch(1)

	for i, wait := range retries {
		if n := e.deliver(); n != 1 {
			t.Fatalf("attempt %d claimed %d", i+1, n)
		}
		d := e.one(ep.ID)
		if d.Status != "pending" || d.Attempts != i+1 || d.NextAttemptAt.Sub(*d.LastAttemptAt) != wait {
			t.Fatalf("after attempt %d = %+v, want backoff %v", i+1, d, wait)
		}
		if n := e.deliver(); n != 0 {
			t.Fatalf("delivery retried before its backoff elapsed")
		}
		e.makeDue()
	}
	e.deliver()
	d := e.one(ep.ID)
	if d.Status != "failed" || d.Attempts != len(retries)+1 || d.NextAttemptAt != nil || *d.LastStatusCode != 503 {
		t.Fatalf("exhausted = %+v", d)
	}
	e.makeDue()
	if n := e.deliver(); n != 0 || len(rcv.got("/h")) != len(retries)+1 {
		t.Fatalf("failed delivery was attempted again")
	}

	rcv.set("/h", http.StatusOK)
	r := e.must(200, http.MethodPost, "/v1/webhook_deliveries/"+typeid.Encode("wd", d.ID)+"/retry", "")
	if r["status"] != "pending" || r["next_attempt_at"] == nil {
		t.Fatalf("manual retry = %v", r)
	}
	e.deliver()
	if d := e.one(ep.ID); d.Status != "succeeded" || d.Attempts != len(retries)+2 || d.LastError != nil {
		t.Fatalf("after manual retry = %+v", d)
	}
}

func TestEventsEdgeNoRetries(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Retries: []time.Duration{}})
	rcv := newEdgeReceiver(t, false)
	rcv.set("/h", http.StatusTeapot)
	ep := e.endpoint(rcv.srv.URL + "/h")
	e.publish(edgeEvent(t, "hold.created"))
	e.dispatch(1)
	e.deliver()
	if d := e.one(ep.ID); d.Status != "failed" || d.Attempts != 1 || *d.LastStatusCode != http.StatusTeapot {
		t.Fatalf("with no retries = %+v", d)
	}
}

func TestEventsEdgeRedirectNotFollowed(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Retries: []time.Duration{time.Hour}})
	target := newEdgeReceiver(t, false)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.srv.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	ep := e.endpoint(redirector.URL + "/hook")
	e.publish(edgeEvent(t, "hold.created"))
	e.dispatch(1)
	e.deliver()
	if hits := target.got(""); len(hits) != 0 {
		t.Fatalf("redirect target received %d requests", len(hits))
	}
	if d := e.one(ep.ID); d.Status != "pending" || *d.LastStatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirected delivery = %+v", d)
	}
}

func TestEventsEdgeTimeout(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Timeout: 200 * time.Millisecond, Retries: []time.Duration{time.Hour}})
	stop := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(func() {
		close(stop)
		slow.Close()
	})
	ep := e.endpoint(slow.URL)
	e.publish(edgeEvent(t, "hold.created"))
	e.dispatch(1)
	start := time.Now()
	e.deliver()
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("deliver took %v with a 200ms timeout", took)
	}
	d := e.one(ep.ID)
	if d.Status != "pending" || d.Attempts != 1 || d.LastStatusCode != nil || d.LastError == nil || !strings.Contains(*d.LastError, "deadline exceeded") {
		t.Fatalf("timed out delivery = %+v (%v)", d, d.LastError)
	}
}

func TestEventsEdgeRefusesNonPublicAddresses(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{Retries: []time.Duration{time.Hour}})
	rcv := newEdgeReceiver(t, true)
	ep := e.endpoint("https://hooks.example.com/hook")
	if _, err := e.pool.Exec(context.Background(), `UPDATE webhook_endpoints SET url = $1 WHERE id = $2`, rcv.srv.URL+"/hook", ep.ID); err != nil {
		t.Fatal(err)
	}
	e.publish(edgeEvent(t, "hold.created"))
	e.dispatch(1)
	e.deliver()
	if hits := rcv.got(""); len(hits) != 0 {
		t.Fatalf("loopback receiver got %d requests", len(hits))
	}
	d := e.one(ep.ID)
	if d.Status != "pending" || d.LastStatusCode != nil || d.LastError == nil || !strings.Contains(*d.LastError, "refusing to deliver to non-public address 127.0.0.1") {
		t.Fatalf("delivery = %+v (%v)", d, d.LastError)
	}
}

type edgeFailingTransport struct{ msg string }

func (f edgeFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(f.msg)
}

func TestEventsEdgeLongErrorsAreTruncated(t *testing.T) {
	t.Parallel()
	const url = "https://hooks.example.com/x"
	prefix := `Post "` + url + `": `
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"short", "dial failed", prefix + "dial failed"},
		{"ascii over limit", strings.Repeat("a", 2000), (prefix + strings.Repeat("a", 2000))[:1024]},
		{"rune split at limit", strings.Repeat("a", 1023-len(prefix)) + "é" + strings.Repeat("b", 10), prefix + strings.Repeat("a", 1023-len(prefix))},
		{"rune ending at limit", strings.Repeat("a", 1022-len(prefix)) + "é" + strings.Repeat("b", 10), prefix + strings.Repeat("a", 1022-len(prefix)) + "é"},
		{"invalid utf8 inside", "bad\xffbyte", prefix + "badbyte"},
	}
	pool := testdb.New(t, map[string]fs.FS{"events": events.Migrations()})
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := edgeEnvOn(t, pool, events.Config{Retries: []time.Duration{time.Hour}, Client: &http.Client{Transport: edgeFailingTransport{tt.msg}}})
			ep := e.endpoint(url)
			e.publish(edgeEvent(t, "hold.created"))
			e.dispatch(1)
			e.deliver()
			d := e.one(ep.ID)
			if d.LastError == nil {
				t.Fatalf("delivery %d = %+v", i, d)
			}
			got := *d.LastError
			if got != tt.want || len(got) > 1024 || !utf8.ValidString(got) {
				t.Fatalf("last_error (%d bytes) = %q, want %q", len(got), got, tt.want)
			}
			if _, err := e.svc.UpdateEndpoint(context.Background(), ep.ID, events.EndpointUpdate{Enabled: new(false)}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEventsEdgeSubscriptions(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true})
	rcv := newEdgeReceiver(t, false)
	subs := map[string][]string{
		"/all":      nil,
		"/star":     {"*"},
		"/exact":    {"hold.created"},
		"/prefix":   {"hold.*"},
		"/nested":   {"balance_monitor.*"},
		"/mixed":    {"account.created", "transaction.*"},
		"/bare":     {"hold"},
		"/disabled": {"*"},
	}
	eps := map[string]events.Endpoint{}
	for path, types := range subs {
		eps[path] = e.endpoint(rcv.srv.URL+path, types...)
	}
	if _, err := e.svc.UpdateEndpoint(context.Background(), eps["/disabled"].ID, events.EndpointUpdate{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	types := []string{"hold.created", "hold.voided", "holds.created", "account.created", "account.closed", "transaction.posted", "balance_monitor.triggered"}
	for _, typ := range types {
		e.publish(edgeEvent(t, typ))
	}
	e.dispatch(len(types))
	for e.deliver() > 0 {
	}

	want := map[string][]string{
		"/all":      types,
		"/star":     types,
		"/exact":    {"hold.created"},
		"/prefix":   {"hold.created", "hold.voided"},
		"/nested":   {"balance_monitor.triggered"},
		"/mixed":    {"account.created", "transaction.posted"},
		"/bare":     nil,
		"/disabled": nil,
	}
	for path, wantTypes := range want {
		t.Run(path, func(t *testing.T) {
			got := map[string]bool{}
			for _, h := range rcv.got(path) {
				var body struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(h.body, &body); err != nil {
					t.Fatal(err)
				}
				got[body.Type] = true
			}
			if len(got) != len(wantTypes) || len(rcv.got(path)) != len(wantTypes) {
				t.Fatalf("received %v, want %v", got, wantTypes)
			}
			for _, typ := range wantTypes {
				if !got[typ] {
					t.Fatalf("missing %s in %v", typ, got)
				}
			}
		})
	}

	t.Run("re-enabling does not backfill", func(t *testing.T) {
		if _, err := e.svc.UpdateEndpoint(context.Background(), eps["/disabled"].ID, events.EndpointUpdate{Enabled: new(true)}); err != nil {
			t.Fatal(err)
		}
		e.dispatch(0)
		e.deliver()
		if n := len(rcv.got("/disabled")); n != 0 {
			t.Fatalf("re-enabled endpoint received %d old events", n)
		}
	})
}

func TestEventsEdgeEndpointChangesAfterDispatch(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true})
	rcv := newEdgeReceiver(t, false)
	ep := e.endpoint(rcv.srv.URL+"/old", "hold.*")
	e.publish(edgeEvent(t, "hold.created"))
	e.dispatch(1)

	off := false
	if _, err := e.svc.UpdateEndpoint(context.Background(), ep.ID, events.EndpointUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if n := e.deliver(); n != 0 {
		t.Fatalf("delivered %d to a disabled endpoint", n)
	}
	if d := e.one(ep.ID); d.Status != "pending" || d.Attempts != 0 {
		t.Fatalf("parked delivery = %+v", d)
	}

	on, moved, narrowed := true, rcv.srv.URL+"/new", []string{"account.*"}
	if _, err := e.svc.UpdateEndpoint(context.Background(), ep.ID, events.EndpointUpdate{Enabled: &on, URL: &moved, EventTypes: &narrowed}); err != nil {
		t.Fatal(err)
	}
	e.deliver()
	if len(rcv.got("/old")) != 0 || len(rcv.got("/new")) != 1 {
		t.Fatalf("old %d, new %d; want the pending delivery sent to the current URL", len(rcv.got("/old")), len(rcv.got("/new")))
	}
	h := rcv.got("/new")[0]
	if !events.Verify(ep.Secret, h.header.Get(events.SignatureHeader), h.body, time.Now(), time.Minute) {
		t.Fatal("secret changed across endpoint updates")
	}
}

func TestEventsEdgeRetention(t *testing.T) {
	t.Parallel()
	e := newEdgeEnv(t, events.Config{AllowInsecureURLs: true, Retention: 24 * time.Hour, PruneBatch: 1, Retries: []time.Duration{}})
	ctx := context.Background()
	old := func(typ string, age time.Duration) events.Event {
		ev := edgeEvent(t, typ)
		ev.ID = edgeOldID(t, time.Now().Add(-age))
		return ev
	}

	early := old("hold.created", 60*24*time.Hour)
	e.publish(early)
	if n, err := e.svc.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("Prune before any dispatch = %d, %v", n, err)
	}

	rcv := newEdgeReceiver(t, false)
	rcv.set("/fail", http.StatusInternalServerError)
	e.endpoint(rcv.srv.URL+"/ok", "hold.*")
	e.endpoint(rcv.srv.URL+"/fail", "account.*")
	parked := e.endpoint(rcv.srv.URL+"/parked", "transaction.*")

	delivered := old("hold.voided", 40*24*time.Hour)
	failed := old("account.created", 40*24*time.Hour)
	unsubscribed := old("ledger.created", 40*24*time.Hour)
	pending := old("transaction.posted", 40*24*time.Hour)
	young := old("hold.created", 23*time.Hour)
	recent := edgeEvent(t, "hold.created")
	e.publish(delivered, failed, unsubscribed, pending, young, recent)
	e.dispatch(7)
	off := false
	if _, err := e.svc.UpdateEndpoint(ctx, parked.ID, events.EndpointUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	for e.deliver() > 0 {
	}
	undispatched := old("hold.created", 40*24*time.Hour)
	e.publish(undispatched)

	n, err := e.svc.Prune(ctx)
	if err != nil || n != 4 {
		t.Fatalf("Prune = %d, %v; want 4", n, err)
	}
	gone := map[string]events.Event{"early": early, "delivered": delivered, "failed": failed, "unsubscribed": unsubscribed}
	kept := map[string]events.Event{"pending": pending, "young": young, "recent": recent, "undispatched": undispatched}
	for name, ev := range gone {
		if _, err := e.svc.Event(ctx, ev.ID); !errors.Is(err, events.ErrNotFound) {
			t.Errorf("%s event survived: %v", name, err)
		}
		if ds, _ := e.svc.ListDeliveries(ctx, events.ListDeliveriesInput{EventID: ev.ID, Limit: 10}); len(ds) != 0 {
			t.Errorf("%s deliveries survived: %d", name, len(ds))
		}
	}
	for name, ev := range kept {
		if _, err := e.svc.Event(ctx, ev.ID); err != nil {
			t.Errorf("%s event pruned: %v", name, err)
		}
	}
	if ds, _ := e.svc.ListDeliveries(ctx, events.ListDeliveriesInput{EventID: pending.ID, Limit: 10}); len(ds) != 1 || ds[0].Status != "pending" {
		t.Fatalf("pending delivery = %+v", ds)
	}
	if n, err := e.svc.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("second Prune = %d, %v", n, err)
	}
	e.must(404, http.MethodGet, "/v1/events/"+typeid.Encode("evt", delivered.ID), "")
}
