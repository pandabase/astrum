package httpx_test

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

type edgeMeta struct {
	Name     string            `json:"name"`
	Count    int               `json:"count"`
	Metadata map[string]string `json:"metadata"`
	Raw      jsontext.Value    `json:"raw,omitzero"`
}

func edgeRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
}

func TestDecodeEdgeJSONRules(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"exact case", `{"name":"a"}`, ""},
		{"wrong case member", `{"Name":"a"}`, `unknown object member name "Name"`},
		{"upper case member", `{"NAME":"a"}`, `unknown object member name "NAME"`},
		{"duplicate top level", `{"name":"a","name":"b"}`, `duplicate object member name "name"`},
		{"duplicate in metadata", `{"metadata":{"k":"1","k":"2"}}`, `duplicate object member name "k"`},
		{"duplicate deep in raw", `{"raw":{"a":{"b":{"c":1,"c":2}}}}`, `duplicate object member name "c"`},
		{"duplicate differing only in case is fine", `{"metadata":{"k":"1","K":"2"}}`, ""},
		{"invalid utf8 in value", "{\"name\":\"\xff\"}", "invalid UTF-8"},
		{"invalid utf8 in metadata key", "{\"metadata\":{\"\xc3\x28\":\"x\"}}", "invalid UTF-8"},
		{"invalid utf8 in raw", "{\"raw\":[\"\xed\xa0\x80\"]}", "invalid UTF-8"},
		{"lone surrogate escape", `{"name":"\ud800"}`, "surrogate"},
		{"trailing object", `{"name":"a"}{}`, "after top-level value"},
		{"trailing garbage", `{"name":"a"} x`, "after top-level value"},
		{"trailing comma", `{"name":"a",}`, "invalid character"},
		{"byte order mark", "\xef\xbb\xbf{\"name\":\"a\"}", "invalid character"},
		{"leading whitespace", " \r\n\t{\"name\":\"a\"}", ""},
		{"array", `[]`, "unmarshal JSON array"},
		{"array of objects", `[{"name":"a"}]`, "unmarshal JSON array"},
		{"number", `42`, "unmarshal JSON number"},
		{"string", `"name"`, "unmarshal JSON string"},
		{"boolean", `true`, "unmarshal JSON boolean"},
		{"null is accepted", `null`, ""},
		{"wrong member type", `{"count":"1"}`, "unmarshal JSON string"},
		{"float into int", `{"count":1.5}`, "unmarshal"},
		{"metadata with non string value", `{"metadata":{"k":1}}`, "unmarshal JSON number"},
		{"metadata nested object", `{"metadata":{"k":{"x":"y"}}}`, "unmarshal JSON object"},
		{"single quotes", `{'name':'a'}`, "invalid character"},
		{"comment", `{"name":"a"/*x*/}`, "invalid character"},
		{"nan literal", `{"count":NaN}`, "invalid character"},
		{"deep raw within limit", `{"raw":` + strings.Repeat("[", 9990) + strings.Repeat("]", 9990) + `}`, ""},
		{"deep raw beyond limit", `{"raw":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`, "exceeded max depth"},
		{"deep unknown member is still rejected", `{"x":` + strings.Repeat(`{"a":`, 50) + `1` + strings.Repeat("}", 50) + `}`, "unknown object member"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got edgeMeta
			err := httpx.Decode(httptest.NewRecorder(), edgeRequest(tt.body), &got)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Decode() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Decode() error = %v, want %q", err, tt.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "invalid request body: ") {
				t.Fatalf("Decode() error = %v, want invalid request body prefix", err)
			}
		})
	}
}

func edgeJSONOfSize(t *testing.T, size int) string {
	t.Helper()
	const head, tail = `{"name":"`, `"}`
	pad := size - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("size %d too small", size)
	}
	return head + strings.Repeat("a", pad) + tail
}

func TestDecodeEdgeBodyLimits(t *testing.T) {
	tests := []struct {
		name    string
		decode  func(http.ResponseWriter, *http.Request, any) error
		size    int
		tooBig  bool
		payload func(*testing.T, int) string
	}{
		{"decode at 1 MiB", httpx.Decode, 1 << 20, false, edgeJSONOfSize},
		{"decode over 1 MiB", httpx.Decode, 1<<20 + 1, true, edgeJSONOfSize},
		{"optional at 1 MiB", httpx.DecodeOptional, 1 << 20, false, edgeJSONOfSize},
		{"optional over 1 MiB", httpx.DecodeOptional, 1<<20 + 1, true, edgeJSONOfSize},
		{"optional whitespace over 1 MiB", httpx.DecodeOptional, 1<<20 + 1, true, func(_ *testing.T, n int) string { return strings.Repeat(" ", n) }},
		{"bulk over 1 MiB", httpx.DecodeBulk, 1<<20 + 1, false, edgeJSONOfSize},
		{"bulk at 16 MiB", httpx.DecodeBulk, httpx.MaxBulkBodyBytes, false, edgeJSONOfSize},
		{"bulk over 16 MiB", httpx.DecodeBulk, httpx.MaxBulkBodyBytes + 1, true, edgeJSONOfSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.payload(t, tt.size)
			if len(body) != tt.size {
				t.Fatalf("payload is %d bytes, want %d", len(body), tt.size)
			}
			var got edgeMeta
			err := tt.decode(httptest.NewRecorder(), edgeRequest(body), &got)
			_, isMax := errors.AsType[*http.MaxBytesError](err)
			if isMax != tt.tooBig {
				t.Fatalf("error = %v, want MaxBytesError %v", err, tt.tooBig)
			}
			if !tt.tooBig && err != nil {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeEdgeOptional(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		want    string
	}{
		{"empty", "", false, "keep"},
		{"whitespace", " \n\t\r ", false, "keep"},
		{"null zeroes the target", "null", false, ""},
		{"empty object", "{}", false, "keep"},
		{"object", `{"name":"x"}`, false, "x"},
		{"unknown member", `{"nope":1}`, true, ""},
		{"array", `[]`, true, ""},
		{"duplicate", `{"name":"x","name":"y"}`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := edgeMeta{Name: "keep"}
			err := httpx.DecodeOptional(httptest.NewRecorder(), edgeRequest(tt.body), &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DecodeOptional() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got.Name != tt.want {
				t.Fatalf("name = %q, want %q", got.Name, tt.want)
			}
		})
	}

	for _, body := range []string{"", "   "} {
		if err := httpx.Decode(httptest.NewRecorder(), edgeRequest(body), &edgeMeta{}); err == nil {
			t.Errorf("Decode(%q) accepted an empty body", body)
		}
	}
}

func edgeServer(t *testing.T, decode func(http.ResponseWriter, *http.Request, any) error) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in edgeMeta
		if err := decode(w, r, &in); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
			return
		}
		httpx.JSON(w, r, http.StatusOK, in)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDecodeEdgeOverHTTP(t *testing.T) {
	srv := edgeServer(t, httpx.Decode)
	tests := []struct {
		name        string
		contentType string
		body        []byte
		status      int
		detail      string
	}{
		{"json", "application/json", []byte(`{"name":"a"}`), 200, ""},
		{"content type is not enforced", "text/plain", []byte(`{"name":"a"}`), 200, ""},
		{"form content type still parsed as json", "application/x-www-form-urlencoded", []byte(`{"name":"a"}`), 200, ""},
		{"no content type", "", []byte(`{"name":"a"}`), 200, ""},
		{"form body", "application/x-www-form-urlencoded", []byte(`name=a`), 400, "invalid character"},
		{"wrong case", "application/json", []byte(`{"Name":"a"}`), 400, "unknown object member"},
		{"duplicate in metadata", "application/json", []byte(`{"metadata":{"a":"1","a":"1"}}`), 400, "duplicate object member"},
		{"invalid utf8", "application/json", []byte("{\"name\":\"\xfe\"}"), 400, "invalid UTF-8"},
		{"trailing data", "application/json", []byte(`{"name":"a"}]`), 400, "after top-level value"},
		{"empty", "application/json", nil, 400, "unexpected EOF"},
		{"array", "application/json", []byte(`[1]`), 400, "unmarshal JSON array"},
		{"number", "application/json", []byte(`7`), 400, "unmarshal JSON number"},
		{"null", "application/json", []byte(`null`), 200, ""},
		{"at limit", "application/json", []byte(edgeJSONOfSize(t, 1<<20)), 200, ""},
		{"over limit", "application/json", []byte(edgeJSONOfSize(t, 1<<20+1)), 400, "request body too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d %s, want %d", resp.StatusCode, raw, tt.status)
			}
			if !bytes.HasSuffix(raw, []byte("}\n")) || bytes.Count(raw, []byte("\n")) != 1 {
				t.Fatalf("body %q does not end with exactly one newline", raw)
			}
			if tt.status == 200 {
				return
			}
			var p httpx.Problem
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("problem %q: %v", raw, err)
			}
			if p.Code != httpx.CodeInvalidRequest || !strings.Contains(p.Detail, tt.detail) || resp.Header.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("problem = %+v (%s), want detail containing %q", p, resp.Header.Get("Content-Type"), tt.detail)
			}
		})
	}
}

func TestDecodeEdgeOversizedClosesConnection(t *testing.T) {
	srv := edgeServer(t, httpx.Decode)
	resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(edgeJSONOfSize(t, 2<<20)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !resp.Close {
		t.Fatalf("status = %d close = %v, want 400 and a closed connection", resp.StatusCode, resp.Close)
	}
}

func TestWriteEdge(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	t.Run("deterministic map order and no html escaping", func(t *testing.T) {
		v := map[string]any{"z": 1, "a": "<&>", "m": map[string]int{"y": 2, "b": 1}}
		var bodies []string
		for range 5 {
			w := httptest.NewRecorder()
			httpx.JSON(w, r, http.StatusOK, v)
			bodies = append(bodies, w.Body.String())
		}
		want := `{"a":"<&>","m":{"b":1,"y":2},"z":1}` + "\n"
		for _, b := range bodies {
			if b != want {
				t.Fatalf("body = %q, want %q", b, want)
			}
		}
	})

	t.Run("nil slice and nil map", func(t *testing.T) {
		w := httptest.NewRecorder()
		httpx.JSON(w, r, http.StatusOK, struct {
			S []int          `json:"s"`
			M map[string]int `json:"m"`
		}{})
		if got := w.Body.String(); got != `{"s":[],"m":{}}`+"\n" {
			t.Fatalf("body = %q", got)
		}
	})

	t.Run("unencodable value becomes an internal error problem", func(t *testing.T) {
		var buf bytes.Buffer
		base := log.NewWithOptions(&buf, log.Options{Formatter: log.JSONFormatter})
		req := r.WithContext(log.WithContext(r.Context(), base))
		w := httptest.NewRecorder()
		httpx.JSON(w, req, http.StatusAccepted, map[string]any{"c": make(chan int)})
		if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("status = %d, content type %q", w.Code, w.Header().Get("Content-Type"))
		}
		var p httpx.Problem
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Code != httpx.CodeInternal || !strings.HasSuffix(w.Body.String(), "\n") {
			t.Fatalf("body = %q, %v", w.Body.String(), err)
		}
		if !strings.Contains(buf.String(), "encode response") {
			t.Fatalf("encode failure not logged: %q", buf.String())
		}
	})

	t.Run("status without text", func(t *testing.T) {
		w := httptest.NewRecorder()
		httpx.Error(w, r, 599, "odd", "")
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["title"] != "" || got["status"] != float64(599) || got["type"] != "urn:astrum:error:odd" {
			t.Fatalf("problem = %v", got)
		}
		if _, ok := got["detail"]; ok {
			t.Fatalf("empty detail serialized: %v", got)
		}
		if _, ok := got["request_id"]; ok {
			t.Fatalf("empty request id serialized: %v", got)
		}
	})

	t.Run("problem echoing invalid utf8 is still valid json", func(t *testing.T) {
		w := httptest.NewRecorder()
		httpx.Error(w, r.WithContext(logger.WithRequestID(r.Context(), "req")), http.StatusForbidden, "forbidden", "a read key cannot POST /v1/\xff")
		var got httpx.Problem
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("status %d body %q is not a problem document: %v", w.Code, w.Body.String(), err)
		}
		if got.Code != "forbidden" || got.Status != http.StatusForbidden {
			t.Fatalf("problem = %+v", got)
		}
	})
}

func TestNewListEdge(t *testing.T) {
	calls := 0
	cursor := func(n int) string { calls++; return string(rune('a' + n)) }

	if l := httpx.NewList([]int{0, 1}, 2, cursor); l.HasMore || l.NextCursor != nil || len(l.Data) != 2 || calls != 0 {
		t.Fatalf("exact page = %+v, cursor calls %d", l, calls)
	}
	l := httpx.NewList([]int{0, 1, 2, 3}, 1, cursor)
	if !l.HasMore || len(l.Data) != 1 || *l.NextCursor != "a" || calls != 1 {
		t.Fatalf("overfull page = %+v, cursor calls %d", l, calls)
	}
	empty := httpx.NewList([]int{}, 5, cursor)
	w := httptest.NewRecorder()
	httpx.JSON(w, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, empty)
	if got := w.Body.String(); got != `{"object":"list","data":[],"has_more":false,"next_cursor":null}`+"\n" {
		t.Fatalf("empty list body = %q", got)
	}
}

func TestPageLimitEdge(t *testing.T) {
	tests := []struct {
		query string
		want  int
		ok    bool
	}{
		{"", httpx.DefaultPageLimit, true},
		{"?limit=", httpx.DefaultPageLimit, true},
		{"?limit=1", 1, true},
		{"?limit=100", 100, true},
		{"?limit=05", 5, true},
		{"?limit=%2B5", 5, true},
		{"?limit=5&limit=500", 5, true},
		{"?limit=500&limit=5", 0, false},
		{"?limit=0", 0, false},
		{"?limit=-1", 0, false},
		{"?limit=-0", 0, false},
		{"?limit=101", 0, false},
		{"?limit=1e2", 0, false},
		{"?limit=1.0", 0, false},
		{"?limit=%205", 0, false},
		{"?limit=0x10", 0, false},
		{"?limit=99999999999999999999", 0, false},
		{"?LIMIT=1", httpx.DefaultPageLimit, true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, err := httpx.PageLimit(httptest.NewRequest(http.MethodGet, "/"+tt.query, nil))
			if (err == nil) != tt.ok || got != tt.want {
				t.Fatalf("PageLimit = %d, %v; want %d ok=%v", got, err, tt.want, tt.ok)
			}
			if err != nil && err.Error() != "limit must be an integer from 1 to 100" {
				t.Fatalf("error = %q", err)
			}
		})
	}
}

func TestLoggingEdgePanicAfterWrite(t *testing.T) {
	h := httpx.Logging(log.New(io.Discard), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "partial")
		panic("late")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusAccepted || w.Body.String() != "partial" {
		t.Fatalf("response = %d %q, want the partial response untouched", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("request id missing after panic")
	}
}

func TestLoggingEdgePanicCarriesRequestID(t *testing.T) {
	h := httpx.Logging(log.New(io.Discard), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var p httpx.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.RequestID == "" || p.RequestID != w.Header().Get("X-Request-ID") || p.Code != httpx.CodeInternal || p.Detail != "" {
		t.Fatalf("problem = %+v, header %q", p, w.Header().Get("X-Request-ID"))
	}
}
