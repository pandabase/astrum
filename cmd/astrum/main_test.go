package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/config"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

func TestServerLifecycle(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    testdb.URL(t),
		HTTPAddr:       freeAddr(t),
		LogLevel:       "error",
		LogFormat:      "text",
		DBMaxConns:     16,
		LedgerWorkers:  4,
		LedgerMaxBatch: 64,
		LedgerSealKey:  "e2e-seal-key-0123456789abcdef-0123456789",
		WebDir:         t.TempDir(),
	}
	if err := os.WriteFile(filepath.Join(cfg.WebDir, "index.html"), []byte("<html>astrum</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()

	base := "http://" + cfg.HTTPAddr
	waitHealthy(t, base, done)

	resp, err := http.Get(base + "/v1/ledgers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d, want 401", resp.StatusCode)
	}

	resp, err = http.Get(base + "/ledgers/ldg_01h455vb4pex5vsknk084sn02q")
	if err != nil {
		t.Fatal(err)
	}
	shell, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(shell) != "<html>astrum</html>" {
		t.Fatalf("client route = %d %q, want the interface shell", resp.StatusCode, shell)
	}

	var out strings.Builder
	if err := runKeys(ctx, cfg, []string{"create", "-name", "e2e"}, &out); err != nil {
		t.Fatal(err)
	}
	token := tokenFrom(t, out.String())
	post := func(t *testing.T, url, key, body string) map[string]any { return postAs(t, token, url, key, body) }

	ledger := post(t, base+"/v1/ledgers", "", `{"name":"main"}`)["id"]
	equity := post(t, base+"/v1/accounts", "", fmt.Sprintf(`{"ledger_id":%q,"code":"equity","currency":"USD","normal_side":"credit","allow_negative":true}`, ledger))["id"]
	cash := post(t, base+"/v1/accounts", "", fmt.Sprintf(`{"ledger_id":%q,"code":"cash","currency":"USD","normal_side":"debit"}`, ledger))["id"]
	if !strings.HasPrefix(cash.(string), "acct_") {
		t.Fatalf("account id = %v, want an acct_ id", cash)
	}

	body := fmt.Sprintf(`{"entries":[
		{"account_id":%q,"side":"debit","amount":"100"},
		{"account_id":%q,"side":"credit","amount":"100"}]}`, cash, equity)
	first := post(t, base+"/v1/transactions", "req-1", body)
	replay := post(t, base+"/v1/transactions", "req-1", body)
	if first["id"] != replay["id"] {
		t.Fatalf("idempotent replay returned %v, want %v", replay["id"], first["id"])
	}

	var report map[string]any
	getJSON(t, token, base+"/v1/integrity", &report)
	if report["ok"] != true {
		t.Fatalf("integrity = %v", report)
	}

	reader := tokenFrom(t, post(t, base+"/v1/api_keys", "", `{"name":"dashboard","role":"read"}`)["secret"].(string))
	getJSON(t, reader, base+"/v1/integrity", &report)
	if report["ok"] != true {
		t.Fatalf("read key integrity = %v", report)
	}
	out.Reset()
	keyID := post(t, base+"/v1/api_keys", "", `{"name":"temp","role":"write"}`)["id"].(string)
	if err := runKeys(ctx, cfg, []string{"revoke", "-id", keyID}, &out); err != nil || !strings.Contains(out.String(), keyID) {
		t.Fatalf("revoke = %q, %v", out.String(), err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("shutdown did not complete")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitHealthy(t *testing.T, base string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("server exited during startup: %v", err)
		default:
		}
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}

func tokenFrom(t *testing.T, text string) string {
	t.Helper()
	for field := range strings.FieldsSeq(text) {
		if strings.HasPrefix(field, "sk_") {
			return field
		}
	}
	t.Fatalf("no key in %q", text)
	return ""
}

func postAs(t *testing.T, token, url, idempotencyKey, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s = %d %v", url, resp.StatusCode, out)
	}
	return out
}

func getJSON(t *testing.T, token, url string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		t.Fatal(err)
	}
}
