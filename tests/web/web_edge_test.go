package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/web"
)

const edgeShell = "<!doctype html><html>shell</html>"

func edgeSite(t *testing.T) (http.Handler, *[]string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "dist")
	files := map[string]string{
		"index.html":               edgeShell,
		"assets/app-3f9a1c.js":     "console.log('app')",
		"assets/app-3f9a1c.css":    "body{}",
		"assets/nested/font.woff2": "font",
		"robots.txt":               "User-agent: *",
		"docs/index.html":          "<html>docs</html>",
		"v1x.txt":                  "not api",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seen []string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api":true}`)
	})
	h, err := web.Handler(dir, api)
	if err != nil {
		t.Fatal(err)
	}
	return h, &seen
}

func edgeServe(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWebEdgeRouting(t *testing.T) {
	h, seen := edgeSite(t)
	const immutable = "public, max-age=31536000, immutable"
	tests := []struct {
		name, method, target string
		status               int
		body, cache, ctype   string
		api, secure          bool
	}{
		{"root", http.MethodGet, "/", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"head root", http.MethodHead, "/", 200, "", "no-cache", "text/html; charset=utf-8", false, true},
		{"deep client route", http.MethodGet, "/ledgers/ldg_1/accounts/acct_2", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"client route with query", http.MethodGet, "/accounts?status=open", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"hashed js", http.MethodGet, "/assets/app-3f9a1c.js", 200, "console.log('app')", immutable, "text/javascript; charset=utf-8", false, true},
		{"hashed css", http.MethodGet, "/assets/app-3f9a1c.css", 200, "body{}", immutable, "text/css; charset=utf-8", false, true},
		{"nested asset", http.MethodGet, "/assets/nested/font.woff2", 200, "font", immutable, "font/woff2", false, true},
		{"asset with query", http.MethodGet, "/assets/app-3f9a1c.js?v=2", 200, "console.log('app')", immutable, "text/javascript; charset=utf-8", false, true},
		{"missing asset gets shell", http.MethodGet, "/assets/app-000000.js", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"assets directory gets shell", http.MethodGet, "/assets", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"root file is not immutable", http.MethodGet, "/robots.txt", 200, "User-agent: *", "", "text/plain; charset=utf-8", false, true},
		{"subdirectory with index gets shell", http.MethodGet, "/docs/", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"index file redirects to directory", http.MethodGet, "/index.html", http.StatusMovedPermanently, "", "", "", false, true},
		{"dot segments are cleaned", http.MethodGet, "/assets/../robots.txt", 200, "User-agent: *", "", "text/plain; charset=utf-8", false, true},
		{"traversal above root", http.MethodGet, "/../secret.txt", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"encoded traversal", http.MethodGet, "/%2e%2e/secret.txt", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"encoded slash traversal", http.MethodGet, "/assets%2F..%2F..%2Fsecret.txt", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"double encoded traversal", http.MethodGet, "/%252e%252e/secret.txt", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"backslash traversal", http.MethodGet, "/..%5csecret.txt", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"nul byte", http.MethodGet, "/robots.txt%00.js", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"api root", http.MethodGet, "/v1", 200, `{"api":true}`, "", "application/json", true, false},
		{"api subtree", http.MethodGet, "/v1/ledgers", 200, `{"api":true}`, "", "application/json", true, false},
		{"api trailing slash", http.MethodGet, "/v1/", 200, `{"api":true}`, "", "application/json", true, false},
		{"api post", http.MethodPost, "/v1/transactions", 200, `{"api":true}`, "", "application/json", true, false},
		{"api delete", http.MethodDelete, "/v1/webhook_endpoints/we_1", 200, `{"api":true}`, "", "application/json", true, false},
		{"api traversal into shell space", http.MethodGet, "/v1/../robots.txt", 200, `{"api":true}`, "", "application/json", true, false},
		{"healthz", http.MethodGet, "/healthz", 200, `{"api":true}`, "", "application/json", true, false},
		{"healthz post", http.MethodPost, "/healthz", 200, `{"api":true}`, "", "application/json", true, false},
		{"healthz lookalike", http.MethodGet, "/healthz/", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"uppercase api", http.MethodGet, "/V1/ledgers", 200, edgeShell, "no-cache", "text/html; charset=utf-8", false, true},
		{"file named like api", http.MethodGet, "/v1x.txt", 200, "not api", "", "text/plain; charset=utf-8", false, true},
		{"post to shell", http.MethodPost, "/ledgers", 405, "Method Not Allowed", "", "text/plain; charset=utf-8", false, false},
		{"put asset", http.MethodPut, "/assets/app-3f9a1c.js", 405, "Method Not Allowed", "", "text/plain; charset=utf-8", false, false},
		{"options", http.MethodOptions, "/", 405, "Method Not Allowed", "", "text/plain; charset=utf-8", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(*seen)
			w := edgeServe(h, tt.method, tt.target, nil)
			if w.Code != tt.status {
				t.Fatalf("status = %d %q, want %d", w.Code, w.Body.String(), tt.status)
			}
			if tt.body != "" && strings.TrimSpace(w.Body.String()) != tt.body {
				t.Fatalf("body = %q, want %q", w.Body.String(), tt.body)
			}
			if tt.method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatalf("HEAD body = %q", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "top secret") {
				t.Fatal("served a file outside the web directory")
			}
			if got := w.Header().Get("Cache-Control"); got != tt.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if tt.ctype != "" && w.Header().Get("Content-Type") != tt.ctype {
				t.Fatalf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tt.ctype)
			}
			if (len(*seen) > before) != tt.api {
				t.Fatalf("api called = %v, want %v", len(*seen) > before, tt.api)
			}
			secure := w.Header().Get("X-Content-Type-Options") == "nosniff" && w.Header().Get("X-Frame-Options") == "DENY" && w.Header().Get("Referrer-Policy") == "no-referrer"
			if secure != tt.secure {
				t.Fatalf("security headers = %v, want %v (%v)", secure, tt.secure, w.Header())
			}
			if tt.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow = %q", w.Header().Get("Allow"))
			}
			if tt.status == http.StatusMovedPermanently && w.Header().Get("Location") != "./" {
				t.Fatalf("Location = %q", w.Header().Get("Location"))
			}
		})
	}
}

func TestWebEdgeConditionalAndRange(t *testing.T) {
	h, _ := edgeSite(t)

	shell := edgeServe(h, http.MethodGet, "/some/route", nil)
	modified := shell.Header().Get("Last-Modified")
	if modified == "" {
		t.Fatal("shell has no Last-Modified")
	}
	cond := edgeServe(h, http.MethodGet, "/other/route", http.Header{"If-Modified-Since": {modified}})
	if cond.Code != http.StatusNotModified || cond.Body.Len() != 0 || cond.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("conditional shell = %d %q %q", cond.Code, cond.Body.String(), cond.Header().Get("Cache-Control"))
	}
	stale := edgeServe(h, http.MethodGet, "/", http.Header{"If-Modified-Since": {time.Unix(0, 0).UTC().Format(http.TimeFormat)}})
	if stale.Code != 200 || stale.Body.String() != edgeShell {
		t.Fatalf("stale conditional = %d", stale.Code)
	}

	asset := edgeServe(h, http.MethodGet, "/assets/app-3f9a1c.js", http.Header{"Range": {"bytes=0-6"}})
	if asset.Code != http.StatusPartialContent || asset.Body.String() != "console" || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("range = %d %q", asset.Code, asset.Body.String())
	}
	bad := edgeServe(h, http.MethodGet, "/assets/app-3f9a1c.js", http.Header{"Range": {"bytes=500-600"}})
	if bad.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range = %d", bad.Code)
	}
}

func TestWebEdgeConstruction(t *testing.T) {
	api := http.NotFoundHandler()
	if _, err := web.Handler(filepath.Join(t.TempDir(), "missing"), api); err == nil || !strings.Contains(err.Error(), "has no index.html") {
		t.Fatalf("missing dir error = %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.htm"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := web.Handler(dir, api); err == nil {
		t.Fatal("a directory without index.html was accepted")
	}
}

func TestWebEdgeShellRemovedAfterStart(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "index.html")
	if err := os.WriteFile(index, []byte(edgeShell), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := web.Handler(dir, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	w := edgeServe(h, http.MethodGet, "/", nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "shell") {
		t.Fatalf("missing shell = %d %q", w.Code, w.Body.String())
	}
}
