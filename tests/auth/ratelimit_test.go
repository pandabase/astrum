package auth_test

import (
	"encoding/json/v2"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/ratelimit"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

func TestRateLimitPerKey(t *testing.T) {
	t.Parallel()
	probe := auth.New(nil, testdb.Logger())
	svc := auth.New(testdb.New(t, map[string]fs.FS{probe.Name(): probe.Migrations()}), testdb.Logger())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("GET /v1/things", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), svc.Middleware([]string{"/healthz"}, ratelimit.New(1, 2).Middleware(mux))))
	t.Cleanup(srv.Close)

	create := func(name string) string {
		_, token, err := svc.Create(t.Context(), auth.CreateInput{Name: name, Role: auth.RoleRead})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	call := func(path, token string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	busy, calm := create("busy"), create("calm")

	for i := range 2 {
		if resp := call("/v1/things", busy); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("request %d within the burst = %d", i, resp.StatusCode)
		}
	}
	resp := call("/v1/things", busy)
	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.UnmarshalRead(resp.Body, &problem); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || problem.Code != "rate_limited" || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("limited request = %d %+v retry-after %q", resp.StatusCode, problem, resp.Header.Get("Retry-After"))
	}
	if resp := call("/v1/things", calm); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("another key was limited: %d", resp.StatusCode)
	}
	for range 3 {
		if resp := call("/healthz", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("public path was limited: %d", resp.StatusCode)
		}
	}
	if resp := call("/v1/things", "sk_invalid"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid key = %d, want 401", resp.StatusCode)
	}
}
