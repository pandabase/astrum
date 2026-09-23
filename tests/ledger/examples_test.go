package tests

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

type docBlock struct {
	script string
	output *string
}

func TestDocExamples(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	pages, err := filepath.Glob("../../docs/examples/*.md")
	if err != nil || len(pages) == 0 {
		t.Fatalf("no example pages found: %v", err)
	}
	for _, page := range pages {
		t.Run(strings.TrimSuffix(filepath.Base(page), ".md"), func(t *testing.T) {
			blocks := parseDoc(t, page)
			e := setupWith(t, ledger.Config{SweepInterval: 50 * time.Millisecond})
			mux := http.NewServeMux()
			e.m.Routes(mux)
			srv := httptest.NewServer(httpx.Logging(testdb.Logger(), mux))
			t.Cleanup(srv.Close)
			runDoc(t, blocks, srv.URL)
			e.verify(t)
		})
	}
}

func parseDoc(t *testing.T, path string) []docBlock {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var (
		blocks []docBlock
		fence  string
		body   strings.Builder
	)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case fence == "" && strings.HasPrefix(line, "```"):
			fence = strings.TrimPrefix(line, "```")
			body.Reset()
		case fence != "" && line == "```":
			switch fence {
			case "sh":
				blocks = append(blocks, docBlock{script: body.String()})
			case "text":
				if len(blocks) == 0 || blocks[len(blocks)-1].output != nil {
					t.Fatalf("%s: text block without a sh block before it", path)
				}
				blocks[len(blocks)-1].output = new(body.String())
			}
			fence = ""
		case fence != "":
			body.WriteString(line + "\n")
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("%s: no sh blocks", path)
	}
	return blocks
}

func runDoc(t *testing.T, blocks []docBlock, url string) {
	t.Helper()
	out := t.TempDir()
	var script strings.Builder
	script.WriteString("set -euo pipefail\n")
	for i, b := range blocks {
		body := strings.ReplaceAll(b.script, "http://localhost:8080", url)
		if b.output == nil {
			script.WriteString("{\n" + body + "} >/dev/null\n")
			continue
		}
		fmt.Fprintf(&script, "{\n%s} >%q\n", body, filepath.Join(out, fmt.Sprint(i)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", script.String())
	cmd.Env = append(os.Environ(), "ASTRUM_KEY=sk_docs")
	if stderr, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, stderr)
	}
	for i, b := range blocks {
		if b.output == nil {
			continue
		}
		got, err := os.ReadFile(filepath.Join(out, fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != *b.output {
			t.Errorf("block %d output:\n%s\nwant:\n%s\nscript:\n%s", i, got, *b.output, b.script)
		}
	}
}
