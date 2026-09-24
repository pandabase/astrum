package ledger

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/pandabase/astrum/internal/money"
)

func TestCrudEdgeMergePatchRFC7396(t *testing.T) {
	tests := []struct {
		name   string
		target string
		patch  string
		want   string
		err    error
	}{
		{"replace member", `{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`, nil},
		{"add member", `{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`, nil},
		{"remove only member", `{"a":"b"}`, `{"a":null}`, `{}`, nil},
		{"remove one member", `{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`, nil},
		{"scalar replaces array", `{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`, nil},
		{"array replaces scalar", `{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`, nil},
		{"nested merge with removal", `{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`, nil},
		{"array of objects replaced", `{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`, nil},
		{"stored null survives an unrelated patch", `{"e":null}`, `{"a":1}`, `{"a":1,"e":null}`, nil},
		{"nulls dropped under a new object", `{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`, nil},
		{"object patch replaces scalar with merged object", `{"a":1}`, `{"a":{"x":null,"y":2}}`, `{"a":{"y":2}}`, nil},
		{"empty target", ``, `{"a":1}`, `{"a":1}`, nil},
		{"empty patch object", `{"a":1}`, `{}`, `{"a":1}`, nil},
		{"whitespace patch is ignored", `{"a":1}`, " \n\t", `{"a":1}`, nil},
		{"null patch resets", `{"a":1}`, `null`, `{}`, nil},
		{"padded null patch resets", `{"a":1}`, "  null\n", `{}`, nil},
		{"array patch", `{"a":"b"}`, `["c"]`, ``, ErrInvalid},
		{"string patch", `{"a":"foo"}`, `"bar"`, ``, ErrInvalid},
		{"number patch", `{}`, `0`, ``, ErrInvalid},
		{"false patch", `{}`, `false`, ``, ErrInvalid},
		{"malformed patch", `{}`, `{"a":`, ``, ErrInvalid},
		{"trailing data", `{}`, `{"a":1}x`, ``, ErrInvalid},
		{"duplicate keys keep the last", `{}`, `{"a":1,"a":2}`, `{"a":2}`, nil},
		{"big numbers keep their digits", `{}`, `{"n":123456789012345678901234567890.5}`, `{"n":123456789012345678901234567890.5}`, nil},
		{"escaped NUL after merge", `{}`, `{"a":"\u0000"}`, ``, ErrInvalid},
		{"merged result over the limit", `{"a":"` + strings.Repeat("x", 9000) + `"}`, `{"b":"` + strings.Repeat("y", 9000) + `"}`, ``, ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, got, err := applyUpdate("n", "d", jsontext.Value(tt.target), UpdateInput{Metadata: jsontext.Value(tt.patch)}, true)
			if !errors.Is(err, tt.err) {
				t.Fatalf("applyUpdate() error = %v, want %v", err, tt.err)
			}
			if err != nil {
				return
			}
			if !jsonEqual(got, jsontext.Value(tt.want)) {
				t.Fatalf("metadata = %s, want %s", got, tt.want)
			}
			if strings.Contains(tt.name, "big numbers") && !strings.Contains(string(got), "123456789012345678901234567890.5") {
				t.Fatalf("number lost precision: %s", got)
			}
		})
	}
}

func TestCrudEdgeMergePatchDoesNotAliasPatch(t *testing.T) {
	patch := map[string]any{"a": map[string]any{"b": jsontext.Value("1")}}
	got := mergePatch(nil, patch)
	got["a"].(map[string]any)["c"] = jsontext.Value("2")
	if _, ok := patch["a"].(map[string]any)["c"]; ok {
		t.Fatal("mergePatch returned a map aliased to the patch")
	}
}

func TestCrudEdgeApplyUpdateNames(t *testing.T) {
	tests := []struct {
		name         string
		in           UpdateInput
		nameRequired bool
		wantName     string
		wantDesc     string
		err          error
	}{
		{"nil fields keep values", UpdateInput{}, true, "n", "d", nil},
		{"clear description", UpdateInput{Description: ptr("")}, true, "n", "", nil},
		{"name at limit", UpdateInput{Name: ptr(strings.Repeat("x", 255))}, true, strings.Repeat("x", 255), "d", nil},
		{"name over limit", UpdateInput{Name: ptr(strings.Repeat("x", 256))}, false, "", "", ErrInvalid},
		{"description at limit", UpdateInput{Description: ptr(strings.Repeat("x", 1024))}, true, "n", strings.Repeat("x", 1024), nil},
		{"description over limit", UpdateInput{Description: ptr(strings.Repeat("x", 1025))}, true, "", "", ErrInvalid},
		{"invalid UTF-8 name", UpdateInput{Name: ptr("\xff")}, false, "", "", ErrInvalid},
		{"NUL description", UpdateInput{Description: ptr("a\x00")}, false, "", "", ErrInvalid},
		{"tab only name when required", UpdateInput{Name: ptr("\t")}, true, "", "", ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, desc, _, err := applyUpdate("n", "d", jsontext.Value(`{}`), tt.in, tt.nameRequired)
			if !errors.Is(err, tt.err) {
				t.Fatalf("applyUpdate() error = %v, want %v", err, tt.err)
			}
			if err == nil && (name != tt.wantName || desc != tt.wantDesc) {
				t.Fatalf("got %q %q, want %q %q", name, desc, tt.wantName, tt.wantDesc)
			}
		})
	}
}

func TestCrudEdgeSameDetails(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"empty and braces", ``, `{}`, true},
		{"key order", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"number spelling", `{"a":1e2}`, `{"a":100.0}`, true},
		{"nested difference", `{"a":{"b":1}}`, `{"a":{"b":2}}`, false},
		{"null versus missing", `{"a":null}`, `{}`, false},
		{"array order", `{"a":[1,2]}`, `{"a":[2,1]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameDetails("n", "d", jsontext.Value(tt.a), "n", "d", jsontext.Value(tt.b)); got != tt.same {
				t.Fatalf("sameDetails() = %v, want %v", got, tt.same)
			}
		})
	}
	if sameDetails("n", "d", nil, "N", "d", nil) || sameDetails("n", "d", nil, "n", "D", nil) {
		t.Fatal("name and description are case sensitive")
	}
}

func TestCrudEdgeValidateCurrency(t *testing.T) {
	tests := []struct {
		code     money.Currency
		exponent int
		ok       bool
	}{
		{"USD", 0, true},
		{"USD", 30, true},
		{"USD", 31, false},
		{"USD", -1, false},
		{"ABCDEFGHIJKLMNOP", 2, true},
		{"ABCDEFGHIJKLMNOPQ", 2, false},
		{"AB", 2, false},
		{"A9_", 2, true},
		{"9AB", 2, false},
		{"_AB", 2, false},
		{"AbC", 2, false},
		{"AB\x00", 2, false},
	}
	for _, tt := range tests {
		err := validateCurrency(CreateCurrencyInput{Code: tt.code, Exponent: tt.exponent})
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("validateCurrency(%q, %d) = %v, want ok=%v", tt.code, tt.exponent, err, tt.ok)
		}
	}
}

func TestCrudEdgeSameTerms(t *testing.T) {
	acc := Account{Currency: "USD", NormalSide: Debit, AllowNegative: true, OverdraftLimit: amt(5), Name: "a", Metadata: jsontext.Value(`{"x":1}`)}
	base := CreateAccountInput{Currency: "USD", NormalSide: Debit, AllowNegative: true, OverdraftLimit: amt(5)}
	tests := []struct {
		name   string
		mutate func(*CreateAccountInput)
		same   bool
	}{
		{"identical", func(in *CreateAccountInput) {}, true},
		{"name and metadata are not terms", func(in *CreateAccountInput) { in.Name = "b"; in.Metadata = jsontext.Value(`{}`) }, true},
		{"currency", func(in *CreateAccountInput) { in.Currency = "EUR" }, false},
		{"side", func(in *CreateAccountInput) { in.NormalSide = Credit }, false},
		{"allow negative", func(in *CreateAccountInput) { in.AllowNegative = false }, false},
		{"overdraft", func(in *CreateAccountInput) { in.OverdraftLimit = amt(6) }, false},
		{"wide overdraft", func(in *CreateAccountInput) { in.OverdraftLimit = money.MaxAmount() }, false},
	}
	for _, tt := range tests {
		in := base
		tt.mutate(&in)
		if got := acc.sameTerms(in); got != tt.same {
			t.Errorf("%s: sameTerms() = %v, want %v", tt.name, got, tt.same)
		}
	}
}
