package typeid_test

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

const edgeValid = "01h455vb4pex5vsknk084sn02q"

func TestEdgeParseRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{"missing separator", "acct" + edgeValid, "expected a acct_ id"},
		{"double separator", "acct__" + edgeValid, "malformed"},
		{"prefix only case differs", "ACCT_" + edgeValid, "expected a acct_ id"},
		{"prefix is a prefix of another", "account_" + edgeValid, "expected a acct_ id"},
		{"leading space", " acct_" + edgeValid, "expected a acct_ id"},
		{"trailing space", "acct_" + edgeValid + " ", "malformed"},
		{"trailing newline", "acct_" + edgeValid + "\n", "malformed"},
		{"25 chars", "acct_" + edgeValid[:25], "malformed"},
		{"27 chars", "acct_" + edgeValid + "0", "malformed"},
		{"empty suffix", "acct_", "malformed"},
		{"first char 8 overflows", "acct_8" + edgeValid[1:], "malformed"},
		{"first char 9 overflows", "acct_9" + edgeValid[1:], "malformed"},
		{"first char z overflows", "acct_z" + edgeValid[1:], "malformed"},
		{"first char uppercase", "acct_A" + edgeValid[1:], "malformed"},
		{"letter i", "acct_" + edgeValid[:25] + "i", "malformed"},
		{"letter l", "acct_" + edgeValid[:25] + "l", "malformed"},
		{"letter o", "acct_" + edgeValid[:25] + "o", "malformed"},
		{"letter u", "acct_" + edgeValid[:25] + "u", "malformed"},
		{"uppercase tail", "acct_" + edgeValid[:25] + "Q", "malformed"},
		{"hyphen", "acct_" + edgeValid[:25] + "-", "malformed"},
		{"high byte", "acct_" + edgeValid[:25] + "\xff", "malformed"},
		{"nul byte", "acct_" + edgeValid[:25] + "\x00", "malformed"},
		{"multibyte rune same byte length", "acct_" + edgeValid[:24] + "é", "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := typeid.Parse("acct", tt.in)
			if !errors.Is(err, typeid.ErrInvalid) || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("Parse(%q) = %v, want ErrInvalid mentioning %q", tt.in, err, tt.msg)
			}
			if id != uuid.Nil {
				t.Fatalf("Parse(%q) returned %s on error", tt.in, id)
			}
		})
	}
}

func TestEdgeFirstCharBoundary(t *testing.T) {
	for _, c := range "01234567" {
		s := "x_" + string(c) + strings.Repeat("0", 25)
		id, err := typeid.Parse("x", s)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", s, err)
		}
		if got := typeid.Encode("x", id); got != s {
			t.Fatalf("Encode(Parse(%q)) = %q", s, got)
		}
	}
	max, err := typeid.Parse("x", "x_7"+strings.Repeat("z", 25))
	if err != nil || max != uuid.Max {
		t.Fatalf("max = %s, %v", max, err)
	}
}

func TestEdgeEveryBitRoundTrips(t *testing.T) {
	seen := map[string]bool{}
	for bit := range 128 {
		var id uuid.UUID
		id[bit/8] = 0x80 >> (bit % 8)
		s := typeid.Encode("b", id)
		if len(s) != 28 || seen[s] {
			t.Fatalf("bit %d encoded as %q (duplicate=%v)", bit, s, seen[s])
		}
		seen[s] = true
		got, err := typeid.Parse("b", s)
		if err != nil || got != id {
			t.Fatalf("bit %d: Parse(%q) = %s, %v", bit, s, got, err)
		}
	}
}

func TestEdgeAlphabetOrderPreserved(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000000"),
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-0000000000ff"),
		uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057"),
		uuid.MustParse("7fffffff-ffff-ffff-ffff-ffffffffffff"),
		uuid.MustParse("80000000-0000-0000-0000-000000000000"),
		uuid.Max,
	}
	for i := 1; i < len(ids); i++ {
		a, b := typeid.Encode("p", ids[i-1]), typeid.Encode("p", ids[i])
		if a >= b {
			t.Fatalf("Encode order broken: %s >= %s", a, b)
		}
	}
}

func TestEdgePrefixes(t *testing.T) {
	id := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057")
	tests := []struct {
		prefix string
		want   string
	}{
		{"", "_" + edgeValid},
		{"a_b", "a_b_" + edgeValid},
		{"key", "key_" + edgeValid},
		{"UPPER", "UPPER_" + edgeValid},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			s := typeid.Encode(tt.prefix, id)
			if s != tt.want {
				t.Fatalf("Encode(%q) = %q, want %q", tt.prefix, s, tt.want)
			}
			got, err := typeid.Parse(tt.prefix, s)
			if err != nil || got != id {
				t.Fatalf("Parse(%q, %q) = %s, %v", tt.prefix, s, got, err)
			}
		})
	}
	if _, err := typeid.Parse("a", "a_b_"+edgeValid); !errors.Is(err, typeid.ErrInvalid) {
		t.Fatalf("underscore prefix accepted by shorter prefix: %v", err)
	}
	if _, err := typeid.Parse("a_b", "a_"+edgeValid); !errors.Is(err, typeid.ErrInvalid) {
		t.Fatalf("shorter prefix accepted by underscore prefix: %v", err)
	}
}

type edgeKeyPrefix struct{}

func (edgeKeyPrefix) Prefix() string { return "key" }

func TestEdgeIDText(t *testing.T) {
	var zero typeid.ID[edgeKeyPrefix]
	if zero.String() != "key_00000000000000000000000000" || zero.UUID() != uuid.Nil {
		t.Fatalf("zero ID = %s", zero)
	}
	txt, err := zero.MarshalText()
	if err != nil || string(txt) != zero.String() {
		t.Fatalf("MarshalText() = %q, %v", txt, err)
	}

	id := typeid.ID[edgeKeyPrefix](uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057"))
	for _, bad := range []string{"", "acct_" + edgeValid, "key_" + strings.ToUpper(edgeValid), "key_" + edgeValid + "x"} {
		before := id
		if err := id.UnmarshalText([]byte(bad)); !errors.Is(err, typeid.ErrInvalid) {
			t.Fatalf("UnmarshalText(%q) = %v", bad, err)
		}
		if id != before {
			t.Fatalf("UnmarshalText(%q) modified the ID on error", bad)
		}
	}

	var v struct {
		ID  typeid.ID[edgeKeyPrefix]  `json:"id"`
		Ptr *typeid.ID[edgeKeyPrefix] `json:"ptr"`
	}
	for _, raw := range []string{`{"id":123}`, `{"id":null,"ptr":"key_nope"}`, `{"id":["key_` + edgeValid + `"]}`} {
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			t.Fatalf("Unmarshal(%s) accepted", raw)
		}
	}
	if err := json.Unmarshal([]byte(`{"id":"key_`+edgeValid+`","ptr":"key_`+edgeValid+`"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.ID.String() != "key_"+edgeValid || v.Ptr == nil || *v.Ptr != v.ID {
		t.Fatalf("decoded = %s %v", v.ID, v.Ptr)
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) != `{"id":"key_`+edgeValid+`","ptr":"key_`+edgeValid+`"}` {
		t.Fatalf("Marshal() = %s, %v", raw, err)
	}
	v.Ptr = nil
	if raw, err = json.Marshal(v); err != nil || string(raw) != `{"id":"key_`+edgeValid+`","ptr":null}` {
		t.Fatalf("Marshal(nil ptr) = %s, %v", raw, err)
	}
	m := map[typeid.ID[edgeKeyPrefix]]int{id: 1}
	if raw, err = json.Marshal(m); err != nil || string(raw) != `{"key_`+edgeValid+`":1}` {
		t.Fatalf("Marshal(map) = %s, %v", raw, err)
	}
}
