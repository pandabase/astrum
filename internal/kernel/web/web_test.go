package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<html>shell</html>")
	write("assets/app-1234.js", "console.log(1)")
	write("favicon.ico", "icon")
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "api "+r.URL.Path) })
	h, err := Handler(dir, api)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, method, path string
		status             int
		body, cache        string
	}{
		{"api", http.MethodGet, "/v1/ledgers", 200, "api /v1/ledgers", ""},
		{"api post", http.MethodPost, "/v1/transactions", 200, "api /v1/transactions", ""},
		{"health", http.MethodGet, "/healthz", 200, "api /healthz", ""},
		{"root", http.MethodGet, "/", 200, "<html>shell</html>", "no-cache"},
		{"client route", http.MethodGet, "/ledgers/ldg_123", 200, "<html>shell</html>", "no-cache"},
		{"lookalike is not api", http.MethodGet, "/v1x", 200, "<html>shell</html>", "no-cache"},
		{"hashed asset", http.MethodGet, "/assets/app-1234.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		{"plain file", http.MethodGet, "/favicon.ico", 200, "icon", ""},
		{"directory", http.MethodGet, "/assets/", 200, "<html>shell</html>", "no-cache"},
		{"no traversal", http.MethodGet, "/../secret.txt", 200, "<html>shell</html>", "no-cache"},
		{"encoded traversal is a client route", http.MethodGet, "/ledgers/x%2F..%2F..%2Fsecret.txt", 200, "<html>shell</html>", "no-cache"},
		{"no writes", http.MethodPost, "/ledgers", 405, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if tt.body != "" && strings.TrimSpace(rec.Body.String()) != tt.body {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.body)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("body leaked a file outside dir: %q", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.cache)
			}
		})
	}

	if _, err := Handler(t.TempDir(), api); err == nil {
		t.Fatal("a directory without index.html was accepted")
	}
}
