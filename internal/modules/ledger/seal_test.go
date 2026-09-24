package ledger

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

func sealFixture() Transaction {
	reverses := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8058")
	return Transaction{
		ID:             uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057"),
		IdempotencyKey: "key-1",
		Description:    "coffee",
		Metadata:       []byte(`{"order":"42"}`),
		ReversesID:     &reverses,
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC),
		Postings: []Posting{
			{AccountID: uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8059"), Side: Debit, Currency: "USD",
				Amount: amt(450), balanceAfter: amt(1450)},
			{AccountID: uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a805a"), Side: Credit, Currency: "USD",
				Amount: amt(450), balanceAfter: amt(-9_000_000_000_000_000_000 + 1)},
		},
	}
}

func TestEntryHashV1(t *testing.T) {
	t.Parallel()
	const want = "100840c7cfe176bc6fe127ea9b13c12a307715783eeecc1ae164f0325db30fab"
	if got := hex.EncodeToString(entryHash(sealFixture())); got != want {
		t.Fatalf("entryHash = %s, want %s", got, want)
	}
}

func TestEntryHashV2(t *testing.T) {
	t.Parallel()
	wide := sealFixture()
	wide.Postings[0].balanceAfter = money.MustParseAmount("18446744073709553066")
	if hex.EncodeToString(entryHash(wide)) == hex.EncodeToString(entryHash(sealFixture())) {
		t.Fatal("a balance beyond int64 hashed like its low 64 bits")
	}
}

func TestEntryHashV3(t *testing.T) {
	t.Parallel()
	base := sealFixture()
	base.EffectiveAt = base.CreatedAt
	base.PostedAt = &base.CreatedAt
	want := hex.EncodeToString(entryHashV3(base))
	if want == hex.EncodeToString(entryHash(base)) {
		t.Fatal("v3 hashed like v1")
	}
	later := base.CreatedAt.Add(time.Second)
	for name, mutate := range map[string]func(*Transaction){
		"ledger":       func(t *Transaction) { t.LedgerID = uuid.New() },
		"external id":  func(t *Transaction) { t.ExternalID = "inv-1" },
		"effective at": func(t *Transaction) { t.EffectiveAt = later },
		"posted at":    func(t *Transaction) { t.PostedAt = &later },
		"amount":       func(t *Transaction) { t.Postings[0].Amount = amt(451) },
	} {
		changed := base
		changed.Postings = append([]Posting(nil), base.Postings...)
		mutate(&changed)
		if hex.EncodeToString(entryHashV3(changed)) == want {
			t.Errorf("changing the %s left the v3 hash unchanged", name)
		}
	}
}
