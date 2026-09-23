package httpx_test

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

type payload struct {
	Name string `json:"name"`
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"valid", `{"name":"astrum"}`, false},
		{"unknown field", `{"name":"astrum","extra":1}`, true},
		{"trailing data", `{"name":"astrum"}{"name":"again"}`, true},
		{"trailing closing brace", `{"name":"astrum"}}`, true},
		{"trailing closing bracket", `{"name":"astrum"}]`, true},
		{"trailing whitespace", "{\"name\":\"astrum\"}\n\t ", false},
		{"malformed", `{"name":`, true},
		{"empty", ``, true},
		{"too large", `{"name":"` + strings.Repeat("a", 1<<20) + `"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			var got payload
			err := httpx.Decode(httptest.NewRecorder(), r, &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got.Name != "astrum" {
				t.Fatalf("Decode() name = %q, want astrum", got.Name)
			}
		})
	}
}

func TestJSONAndError(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	w := httptest.NewRecorder()
	httpx.JSON(w, r, http.StatusCreated, payload{Name: "astrum"})
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if body := strings.TrimSpace(w.Body.String()); body != `{"name":"astrum"}` {
		t.Errorf("body = %s", body)
	}

	w = httptest.NewRecorder()
	httpx.Error(w, r.WithContext(logger.WithRequestID(r.Context(), "req-1")), http.StatusNotFound, httpx.CodeNotFound, "missing")
	var got httpx.Problem
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := httpx.Problem{Type: "urn:astrum:error:not_found", Title: "Not Found", Status: 404, Code: "not_found", Detail: "missing", RequestID: "req-1"}
	if w.Code != http.StatusNotFound || got != want {
		t.Errorf("Error() = %d %+v", w.Code, got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

func TestLoggingRequestID(t *testing.T) {
	var seen string
	h := httpx.Logging(log.New(io.Discard), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logger.RequestID(r.Context())
		w.WriteHeader(http.StatusAccepted)
	}))

	t.Run("generated", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

		id := w.Header().Get("X-Request-ID")
		if parsed, err := uuid.Parse(id); err != nil || parsed.Version() != 4 || len(id) != 36 {
			t.Fatalf("generated request id = %q, want a UUIDv4", id)
		}
		if seen != id {
			t.Fatalf("context request id = %q, header = %q", seen, id)
		}
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
	})

	t.Run("upstream uuidv4 propagated", func(t *testing.T) {
		const upstream = "3f6b0c1e-8d2a-4c5b-9e7f-0a1b2c3d4e5f"
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Request-ID", upstream)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if got := w.Header().Get("X-Request-ID"); got != upstream || seen != upstream {
			t.Fatalf("request id header = %q, context = %q, want %s", got, seen, upstream)
		}
	})

	for name, upstream := range map[string]string{
		"not a uuid":    "upstream-1",
		"oversized":     strings.Repeat("x", 200),
		"uuidv7":        "01890a5d-ac96-774b-bcce-b302099a8057",
		"braced uuidv4": "{3f6b0c1e-8d2a-4c5b-9e7f-0a1b2c3d4e5f}",
		"urn uuidv4":    "urn:uuid:3f6b0c1e-8d2a-4c5b-9e7f-0a1b2c3d4e5f",
	} {
		t.Run(name+" replaced", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("X-Request-ID", upstream)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			got := w.Header().Get("X-Request-ID")
			if parsed, err := uuid.Parse(got); err != nil || parsed.Version() != 4 || got == upstream || len(got) != 36 {
				t.Fatalf("request id = %q, want a fresh UUIDv4", got)
			}
		})
	}
}

func TestLoggingLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	base := log.NewWithOptions(&buf, log.Options{Formatter: log.JSONFormatter})
	h := httpx.Logging(base, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusTeapot, "teapot", "short and stout")
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/brew", nil))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("decode log %q: %v", buf.String(), err)
	}
	checks := map[string]any{
		"prefix": "http",
		"level":  "warn",
		"method": "POST",
		"path":   "/brew",
		"status": float64(http.StatusTeapot),
	}
	for k, want := range checks {
		if entry[k] != want {
			t.Errorf("log[%s] = %v, want %v", k, entry[k], want)
		}
	}
}

func TestLoggingRecoversPanic(t *testing.T) {
	var buf bytes.Buffer
	base := log.NewWithOptions(&buf, log.Options{Formatter: log.JSONFormatter})
	h := httpx.Logging(base, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("panic not logged: %q", buf.String())
	}
}

func TestDecodeOptional(t *testing.T) {
	var got payload
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	if err := httpx.DecodeOptional(httptest.NewRecorder(), r, &got); err != nil {
		t.Fatalf("empty body: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}}`))
	if err := httpx.DecodeOptional(httptest.NewRecorder(), r, &got); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestNewList(t *testing.T) {
	cursor := func(n int) string { return strconv.Itoa(n) }
	if l := httpx.NewList([]int{1, 2, 3}, 2, cursor); !l.HasMore || len(l.Data) != 2 || *l.NextCursor != "2" {
		t.Fatalf("full page = %+v", l)
	}
	if l := httpx.NewList([]int{1}, 2, cursor); l.HasMore || l.NextCursor != nil {
		t.Fatalf("last page = %+v", l)
	}
	if l := httpx.NewList[int](nil, 2, cursor); l.Data == nil || l.Object != "list" {
		t.Fatalf("empty page = %+v", l)
	}
}

func TestPageLimit(t *testing.T) {
	for query, want := range map[string]int{"": 25, "?limit=1": 1, "?limit=100": 100, "?limit=0": 0, "?limit=101": 0, "?limit=x": 0} {
		got, err := httpx.PageLimit(httptest.NewRequest(http.MethodGet, "/"+query, nil))
		if got != want || (want == 0) != (err != nil) {
			t.Errorf("PageLimit(%q) = %d, %v; want %d", query, got, err, want)
		}
	}
}
