package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/pandabase/astrum/internal/bench"
)

type fakeServer struct {
	url       string
	txns      atomic.Int64
	failTxns  bool
	integrity string
	onTxn     func(n int64)
	mu        sync.Mutex
	keys      []string
}

func newFake(t *testing.T, configure func(*fakeServer)) *fakeServer {
	t.Helper()
	f := &fakeServer{integrity: `{"ok":true,"issues":[]}`}
	if configure != nil {
		configure(f)
	}
	var accounts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.keys = append(f.keys, r.Header.Get("Authorization"))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/me":
			io.WriteString(w, `{"role":"write"}`)
		case "/v1/ledgers":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"ldg_bench"}`)
		case "/v1/accounts":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"acct_%d"}`, accounts.Add(1))
		case "/v1/integrity":
			io.WriteString(w, f.integrity)
		default:
			n := f.txns.Add(1)
			if f.onTxn != nil {
				f.onTxn(n)
			}
			if f.failTxns {
				w.WriteHeader(http.StatusUnprocessableEntity)
				io.WriteString(w, `{"code":"insufficient_funds","detail":"no"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"txn_%d"}`, n)
		}
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fakeServer) authHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

func runArgs(t *testing.T, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(ctx, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ASTRUM_URL", "")
	t.Setenv("ASTRUM_KEY", "")
}

func TestEdgeHelp(t *testing.T) {
	clearEnv(t)
	for _, arg := range []string{"-h", "-help", "--help"} {
		t.Run(arg, func(t *testing.T) {
			stdout, stderr, err := runArgs(t, context.Background(), arg)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("run(%s) = %v, want flag.ErrHelp", arg, err)
			}
			if stdout != "" {
				t.Fatalf("help wrote to stdout: %q", stdout)
			}
			for _, want := range []string{
				"Load-tests a running Astrum server",
				"usage: astrum-bench [flags]",
				"scenarios: transfer, batch, pending, hold, read",
				"-concurrency int",
				"(default 32)",
				"-url string",
				"(default \"http://localhost:8080\")",
				"-json",
				"-quiet",
				"-seed uint",
			} {
				if !strings.Contains(stderr, want) {
					t.Errorf("help lacks %q:\n%s", want, stderr)
				}
			}
		})
	}
}

func TestEdgeHelpShowsEnvDefaults(t *testing.T) {
	t.Setenv("ASTRUM_URL", "http://bench.example:9000")
	t.Setenv("ASTRUM_KEY", "")
	_, stderr, err := runArgs(t, context.Background(), "-h")
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(stderr, `(default "http://bench.example:9000")`) {
		t.Fatalf("run(-h) = %v:\n%s", err, stderr)
	}
}

func TestEdgeFlagErrors(t *testing.T) {
	clearEnv(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"-nope"}, "flag provided but not defined: -nope"},
		{"non numeric concurrency", []string{"-concurrency", "abc"}, `invalid value "abc" for flag -concurrency`},
		{"float concurrency", []string{"-concurrency", "1.5"}, "invalid value"},
		{"overflowing concurrency", []string{"-concurrency", "99999999999999999999"}, "value out of range"},
		{"negative seed", []string{"-seed", "-1"}, `invalid value "-1" for flag -seed`},
		{"bad rate", []string{"-rate", "fast"}, "invalid value"},
		{"duration without unit", []string{"-duration", "5"}, "invalid value"},
		{"bad bool", []string{"-json=maybe"}, "invalid boolean value"},
		{"missing flag value", []string{"-url"}, "flag needs an argument: -url"},
		{"positional argument", []string{"extra"}, "unexpected arguments [extra]"},
		{"positional after flags", []string{"-operations", "1", "a", "b"}, "unexpected arguments [a b]"},
		{"flag after positional", []string{"a", "-json"}, "unexpected arguments [a -json]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runArgs(t, context.Background(), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run(%v) = %v, want %q", tt.args, err, tt.want)
			}
			if errors.Is(err, flag.ErrHelp) {
				t.Fatalf("run(%v) = ErrHelp", tt.args)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing on error", stdout)
			}
			if strings.Contains(err.Error(), "flag") && !strings.Contains(stderr, "usage: astrum-bench") {
				t.Fatalf("flag error printed no usage:\n%s", stderr)
			}
		})
	}
}

func TestEdgeValidation(t *testing.T) {
	clearEnv(t)
	f := newFake(t, nil)
	base := []string{"-url", f.url, "-key", "sk_x", "-operations", "1", "-quiet"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"zero concurrency", []string{"-concurrency", "0"}, "concurrency must be at least 1"},
		{"negative concurrency", []string{"-concurrency", "-4"}, "concurrency must be at least 1"},
		{"one account", []string{"-accounts", "1"}, "at least 2 accounts"},
		{"zero accounts", []string{"-accounts", "0"}, "at least 2 accounts"},
		{"negative batch", []string{"-batch", "-1"}, "batch size must be 1-1000"},
		{"huge batch", []string{"-batch", "1001"}, "batch size must be 1-1000"},
		{"negative rate", []string{"-rate", "-1"}, "rate cannot be negative"},
		{"unknown scenario", []string{"-scenario", "mint"}, `unknown scenario "mint"`},
		{"empty url", []string{"-url", ""}, "url is required"},
		{"slash url", []string{"-url", "/"}, "url is required"},
		{"empty key", []string{"-key", ""}, "API key"},
		{"no stop condition", []string{"-operations", "0", "-duration", "0"}, "set a duration"},
		{"negative operations", []string{"-operations", "-5", "-duration", "0"}, "set a duration"},
		{"negative duration", []string{"-operations", "0", "-duration", "-1s"}, "set a duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runArgs(t, context.Background(), append(append([]string{}, base...), tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run(%v) = %v, want %q", tt.args, err, tt.want)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want no report", stdout)
			}
		})
	}
	if n := f.txns.Load(); n != 0 {
		t.Fatalf("invalid configurations sent %d operations", n)
	}
}

func TestEdgeZeroBatchUsesDefault(t *testing.T) {
	clearEnv(t)
	f := newFake(t, nil)
	stdout, _, err := runArgs(t, context.Background(), "-url", f.url, "-key", "k", "-scenario", "batch", "-batch", "0", "-operations", "1", "-accounts", "2", "-json", "-quiet")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"batch_size": 100,`) || !strings.Contains(stdout, `"transactions": 100,`) {
		t.Fatalf("report = %s", stdout)
	}
}

func TestEdgeEnvFallbacks(t *testing.T) {
	f := newFake(t, nil)
	tests := []struct {
		name    string
		envURL  string
		envKey  string
		args    []string
		wantKey string
	}{
		{"env only", f.url, "sk_env", nil, "Bearer sk_env"},
		{"flags override env", "http://127.0.0.1:1", "sk_env", []string{"-url", f.url, "-key", "sk_flag"}, "Bearer sk_flag"},
		{"flag url env key", "http://127.0.0.1:1", "sk_env", []string{"-url", f.url}, "Bearer sk_env"},
		{"env url flag key", f.url, "", []string{"-key", "sk_flag"}, "Bearer sk_flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ASTRUM_URL", tt.envURL)
			t.Setenv("ASTRUM_KEY", tt.envKey)
			before := len(f.authHeaders())
			args := append([]string{"-operations", "2", "-accounts", "2", "-quiet"}, tt.args...)
			if _, stderr, err := runArgs(t, context.Background(), args...); err != nil {
				t.Fatalf("run(%v) = %v\n%s", args, err, stderr)
			}
			seen := f.authHeaders()[before:]
			if len(seen) == 0 {
				t.Fatal("no requests reached the server")
			}
			for _, h := range seen {
				if h != tt.wantKey {
					t.Fatalf("Authorization = %q, want %q", h, tt.wantKey)
				}
			}
		})
	}
}

func TestEdgeEnvOr(t *testing.T) {
	const key = "ASTRUM_BENCH_EDGE_ENV_OR"
	tests := []struct {
		value string
		want  string
	}{
		{"", "fallback"},
		{" ", " "},
		{"set", "set"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.value), func(t *testing.T) {
			t.Setenv(key, tt.value)
			if got := envOr(key, "fallback"); got != tt.want {
				t.Fatalf("envOr = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		t.Setenv(key, "x")
		os.Unsetenv(key)
		if got := envOr(key, "fallback"); got != "fallback" {
			t.Fatalf("envOr = %q", got)
		}
	})
}

func TestEdgeJSONOutput(t *testing.T) {
	clearEnv(t)
	f := newFake(t, nil)
	stdout, stderr, err := runArgs(t, context.Background(), "-url", f.url, "-key", "k", "-operations", "5", "-concurrency", "2", "-accounts", "3", "-seed", "42", "-rate", "1000", "-verify", "-json", "-quiet")
	if err != nil {
		t.Fatalf("run = %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "{\n  \"scenario\": \"transfer\",\n") || !strings.HasSuffix(stdout, "}\n") || strings.HasSuffix(stdout, "}\n\n") {
		t.Fatalf("stdout is not a single indented JSON document:\n%s", stdout)
	}
	if strings.Contains(stderr, "{") {
		t.Fatalf("JSON leaked to stderr:\n%s", stderr)
	}

	dec := jsontext.NewDecoder(strings.NewReader(stdout))
	var (
		top     []string
		latency []string
		values  = map[string]jsontext.Value{}
	)
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		t.Fatalf("first token = %v, %v", tok, err)
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			t.Fatal(err)
		}
		name := tok.String()
		top = append(top, name)
		if name == "latency_ns" {
			if _, err := dec.ReadToken(); err != nil {
				t.Fatal(err)
			}
			for dec.PeekKind() != '}' {
				k, err := dec.ReadToken()
				if err != nil {
					t.Fatal(err)
				}
				key := k.String()
				v, err := dec.ReadValue()
				if err != nil {
					t.Fatal(err)
				}
				latency = append(latency, key)
				if v.Kind() != '0' || bytes.ContainsAny(v, ".eE-") {
					t.Fatalf("latency %s = %s, want a non-negative integer of nanoseconds", key, v)
				}
			}
			if _, err := dec.ReadToken(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		v, err := dec.ReadValue()
		if err != nil {
			t.Fatal(err)
		}
		values[name] = v.Clone()
	}
	wantTop := []string{"scenario", "ledger_id", "concurrency", "accounts", "rate", "seed", "elapsed_ns", "operations", "succeeded", "failed", "transactions", "operations_per_second", "transactions_per_second", "errors", "latency_ns", "integrity"}
	if strings.Join(top, ",") != strings.Join(wantTop, ",") {
		t.Fatalf("keys = %v\nwant   %v", top, wantTop)
	}
	if strings.Join(latency, ",") != "mean,p50,p90,p99,p999,max" {
		t.Fatalf("latency keys = %v", latency)
	}
	checks := map[string]string{
		"seed":        "42",
		"operations":  "5",
		"succeeded":   "5",
		"failed":      "0",
		"concurrency": "2",
		"accounts":    "3",
		"rate":        "1000",
		"ledger_id":   `"ldg_bench"`,
		"errors":      "{}",
	}
	for k, want := range checks {
		if got := string(values[k]); got != want {
			t.Errorf("%s = %s, want %s", k, got, want)
		}
	}
	if e := values["elapsed_ns"]; e.Kind() != '0' || bytes.ContainsAny(e, ".eE-") || string(e) == "0" {
		t.Errorf("elapsed_ns = %s, want positive integer nanoseconds", e)
	}
	if !strings.Contains(string(values["integrity"]), `"ok": true`) {
		t.Errorf("integrity = %s", values["integrity"])
	}
}

func TestEdgeJSONErrorsSorted(t *testing.T) {
	clearEnv(t)
	f := newFake(t, func(f *fakeServer) { f.failTxns = true })
	stdout, _, err := runArgs(t, context.Background(), "-url", f.url, "-key", "k", "-operations", "3", "-accounts", "2", "-json", "-quiet")
	if err == nil || err.Error() != "3 of 3 operations failed" {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(stdout, "\"errors\": {\n    \"insufficient_funds\": 3\n  },") {
		t.Fatalf("report = %s", stdout)
	}
}

func TestEdgeTextOutput(t *testing.T) {
	clearEnv(t)
	f := newFake(t, nil)
	stdout, stderr, err := runArgs(t, context.Background(), "-url", f.url, "-key", "k", "-operations", "4", "-accounts", "2", "-seed", "9", "-quiet")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scenario    transfer\n", "ledger      ldg_bench\n", "32 workers, 2 accounts, seed 9", "4 ok, 0 failed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.HasPrefix(stdout, "{") || !strings.Contains(stderr, "starting") || !strings.Contains(stderr, "bench") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestEdgeQuietAndProgress(t *testing.T) {
	f := newFake(t, nil)
	for _, quiet := range []bool{true, false} {
		t.Run(fmt.Sprintf("quiet=%v", quiet), func(t *testing.T) {
			t.Parallel()
			args := []string{"-url", f.url, "-key", "k", "-duration", "1100ms", "-concurrency", "1", "-accounts", "2", "-rate", "50", "-quiet=" + fmt.Sprint(quiet)}
			_, stderr, err := runArgs(t, context.Background(), args...)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(stderr, "progress"); got == quiet {
				t.Fatalf("quiet=%v progress logged=%v:\n%s", quiet, got, stderr)
			}
			if !strings.Contains(stderr, "starting") {
				t.Fatalf("stderr lacks the start line:\n%s", stderr)
			}
		})
	}
}

func TestEdgeExitOutcomes(t *testing.T) {
	clearEnv(t)
	tests := []struct {
		name      string
		configure func(*fakeServer)
		verify    bool
		want      string
		report    string
	}{
		{"all good", nil, true, "", "integrity   ok"},
		{"failed operations", func(f *fakeServer) { f.failTxns = true }, false, "4 of 4 operations failed", "insufficient_funds × 4"},
		{"failed integrity", func(f *fakeServer) { f.integrity = `{"ok":false,"issues":["drift"]}` }, true, "integrity check failed", "FAILED: [drift]"},
		{"integrity wins over failures", func(f *fakeServer) {
			f.failTxns = true
			f.integrity = `{"ok":false,"issues":["x"]}`
		}, true, "integrity check failed", "0 ok, 4 failed"},
		{"integrity endpoint broken", func(f *fakeServer) { f.integrity = `nope` }, true, "bench: integrity check", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, tt.configure)
			args := []string{"-url", f.url, "-key", "k", "-operations", "4", "-concurrency", "1", "-accounts", "2", "-quiet"}
			if tt.verify {
				args = append(args, "-verify")
			}
			stdout, _, err := runArgs(t, context.Background(), args...)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("run = %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("run = %v, want %q", err, tt.want)
			}
			if tt.report == "" {
				if stdout != "" {
					t.Fatalf("stdout = %q, want no report when the run errored", stdout)
				}
				return
			}
			if !strings.Contains(stdout, tt.report) {
				t.Fatalf("stdout lacks %q:\n%s", tt.report, stdout)
			}
		})
	}
}

func TestEdgeOutcome(t *testing.T) {
	l := log.New(io.Discard)
	tests := []struct {
		name   string
		report bench.Report
		want   string
	}{
		{"clean", bench.Report{Operations: 3, Succeeded: 3}, ""},
		{"no operations", bench.Report{}, ""},
		{"integrity ok", bench.Report{Integrity: &bench.Integrity{OK: true}}, ""},
		{"failures", bench.Report{Operations: 10, Failed: 1}, "1 of 10 operations failed"},
		{"integrity failed", bench.Report{Integrity: &bench.Integrity{}}, "integrity check failed"},
		{"integrity beats failures", bench.Report{Operations: 2, Failed: 2, Integrity: &bench.Integrity{}}, "integrity check failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := outcome(l, tt.report)
			if (tt.want == "" && err != nil) || (tt.want != "" && (err == nil || err.Error() != tt.want)) {
				t.Fatalf("outcome = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEdgeInterruptedMidRun(t *testing.T) {
	clearEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFake(t, func(f *fakeServer) {
		f.onTxn = func(n int64) {
			if n == 3 {
				cancel()
			}
		}
	})
	stdout, stderr, err := runArgs(t, ctx, "-url", f.url, "-key", "k", "-operations", "1000", "-concurrency", "1", "-accounts", "2", "-json", "-quiet")
	if err != nil {
		t.Fatalf("run = %v, want the partial report to count as success", err)
	}
	if !strings.Contains(stderr, "interrupted; reporting what completed") {
		t.Fatalf("stderr lacks the interruption warning:\n%s", stderr)
	}
	if !strings.Contains(stdout, `"ledger_id": "ldg_bench"`) || strings.Contains(stdout, `"operations": 0,`) || strings.Contains(stdout, `"operations": 1000,`) {
		t.Fatalf("partial report = %s", stdout)
	}
}

func TestEdgeInterruptedDuringSetup(t *testing.T) {
	clearEnv(t)
	f := newFake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout, stderr, err := runArgs(t, ctx, "-url", f.url, "-key", "k", "-operations", "5", "-json", "-quiet")
	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(stderr, "interrupted") || !strings.Contains(stdout, `"ledger_id": ""`) || !strings.Contains(stdout, `"scenario": ""`) {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestEdgeWriteFailure(t *testing.T) {
	clearEnv(t)
	f := newFake(t, nil)
	for _, asJSON := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%v", asJSON), func(t *testing.T) {
			args := []string{"-url", f.url, "-key", "k", "-operations", "1", "-accounts", "2", "-quiet", "-json=" + fmt.Sprint(asJSON)}
			if err := run(context.Background(), args, failingWriter{}, io.Discard); err == nil || !strings.Contains(err.Error(), "broken pipe") {
				t.Fatalf("run = %v, want the stdout write error", err)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

const mainArgsEnv = "ASTRUM_BENCH_EDGE_MAIN_ARGS"

func TestEdgeMainProcess(t *testing.T) {
	raw, ok := os.LookupEnv(mainArgsEnv)
	if !ok {
		t.Skip("helper process")
	}
	os.Args = append([]string{"astrum-bench"}, strings.Split(raw, "\x1f")...)
	main()
	os.Exit(0)
}

func TestEdgeMainExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	f := newFake(t, nil)
	failing := newFake(t, func(f *fakeServer) { f.failTxns = true })
	tests := []struct {
		name   string
		args   []string
		code   int
		stderr string
	}{
		{"help", []string{"-h"}, 0, "usage: astrum-bench"},
		{"bad flag", []string{"-nope"}, 1, "astrum-bench: flag provided but not defined: -nope"},
		{"invalid config", []string{"-url", f.url, "-key", "k", "-concurrency", "0"}, 1, "astrum-bench: bench: concurrency must be at least 1"},
		{"success", []string{"-url", f.url, "-key", "k", "-operations", "2", "-accounts", "2", "-quiet"}, 0, "starting"},
		{"failed operations", []string{"-url", failing.url, "-key", "k", "-operations", "2", "-accounts", "2", "-quiet"}, 1, "astrum-bench: 2 of 2 operations failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestEdgeMainProcess$")
			cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join(tt.args, "\x1f"), "ASTRUM_URL=", "ASTRUM_KEY=")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tt.code || !strings.Contains(stderr.String(), tt.stderr) {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, tt.code, stderr.String())
			}
		})
	}
}
