package typeid_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

func TestSpecVectors(t *testing.T) {
	tests := []struct {
		prefix, text, uuid string
	}{
		{"prefix", "prefix_01h455vb4pex5vsknk084sn02q", "01890a5d-ac96-774b-bcce-b302099a8057"},
		{"nil", "nil_00000000000000000000000000", "00000000-0000-0000-0000-000000000000"},
		{"max", "max_7zzzzzzzzzzzzzzzzzzzzzzzzz", "ffffffff-ffff-ffff-ffff-ffffffffffff"},
		{"one", "one_00000000000000000000000001", "00000000-0000-0000-0000-000000000001"},
		{"ten", "ten_0000000000000000000000000a", "00000000-0000-0000-0000-00000000000a"},
		{"big", "big_00000000000000000000000020", "00000000-0000-0000-0000-000000000040"},
	}
	for _, tt := range tests {
		want := uuid.MustParse(tt.uuid)
		if got := typeid.Encode(tt.prefix, want); got != tt.text {
			t.Errorf("Encode(%s) = %s, want %s", tt.uuid, got, tt.text)
		}
		got, err := typeid.Parse(tt.prefix, tt.text)
		if err != nil || got != want {
			t.Errorf("Parse(%s) = %s, %v; want %s", tt.text, got, err, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"acct",
		"acct_",
		"txn_01h455vb4pex5vsknk084sn02q",
		"acct_01h455vb4pex5vsknk084sn02",
		"acct_01h455vb4pex5vsknk084sn02qq",
		"acct_81h455vb4pex5vsknk084sn02q",
		"acct_01H455VB4PEX5VSKNK084SN02Q",
		"acct_01h455vb4pex5vsknk084sn0uq",
		"01890a5d-ac96-774b-bcce-b302099a8057",
	} {
		if _, err := typeid.Parse("acct", s); !errors.Is(err, typeid.ErrInvalid) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalid", s, err)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("0123456789abcdef"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) != 16 {
			return
		}
		id := uuid.UUID(b)
		got, err := typeid.Parse("x", typeid.Encode("x", id))
		if err != nil || got != id {
			t.Fatalf("round trip %s = %s, %v", id, got, err)
		}
	})
}

type accountPrefix struct{}

func (accountPrefix) Prefix() string { return "acct" }

func TestIDJSON(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	var v struct {
		ID typeid.ID[accountPrefix] `json:"id"`
	}
	v.ID = typeid.ID[accountPrefix](id)
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	v.ID = typeid.ID[accountPrefix]{}
	if err := json.Unmarshal(raw, &v); err != nil || v.ID.UUID() != id {
		t.Fatalf("round trip %s = %s, %v", raw, v.ID, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"txn_01h455vb4pex5vsknk084sn02q"}`), &v); err == nil {
		t.Fatal("wrong prefix accepted")
	}
}
