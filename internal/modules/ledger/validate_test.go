package ledger

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateAccount(t *testing.T) {
	t.Parallel()
	valid := CreateAccountInput{LedgerID: uuid.New(), Code: "assets:cash", Currency: "USD", NormalSide: Debit}

	tests := []struct {
		name    string
		mutate  func(*CreateAccountInput)
		wantErr bool
	}{
		{"valid", func(*CreateAccountInput) {}, false},
		{"credit normal side", func(in *CreateAccountInput) { in.NormalSide = Credit }, false},
		{"overdraft", func(in *CreateAccountInput) { in.OverdraftLimit = amt(500) }, false},
		{"empty code", func(in *CreateAccountInput) { in.Code = "" }, true},
		{"blank code", func(in *CreateAccountInput) { in.Code = "   " }, true},
		{"code too long", func(in *CreateAccountInput) { in.Code = strings.Repeat("a", maxCodeLen+1) }, true},
		{"code with NUL", func(in *CreateAccountInput) { in.Code = "a\x00b" }, true},
		{"code invalid utf8", func(in *CreateAccountInput) { in.Code = "a\xffb" }, true},
		{"bad currency", func(in *CreateAccountInput) { in.Currency = "usd" }, true},
		{"unknown side", func(in *CreateAccountInput) { in.NormalSide = "sideways" }, true},
		{"negative overdraft", func(in *CreateAccountInput) { in.OverdraftLimit = amt(-1) }, true},
		{"no ledger", func(in *CreateAccountInput) { in.LedgerID = uuid.Nil }, true},
		{"named", func(in *CreateAccountInput) { in.Name, in.Metadata = "Cash", []byte(`{"region":"eu"}`) }, false},
		{"name too long", func(in *CreateAccountInput) { in.Name = strings.Repeat("n", maxNameLen+1) }, true},
		{"metadata not an object", func(in *CreateAccountInput) { in.Metadata = []byte(`[1]`) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.mutate(&in)
			err := validateAccount(in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAccount() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidatePost(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	valid := func() PostInput {
		return PostInput{
			IdempotencyKey: "payment-1",
			Postings: []Posting{
				{AccountID: a, Side: Debit, Amount: amt(100)},
				{AccountID: b, Side: Credit, Amount: amt(100)},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*PostInput)
		wantErr bool
	}{
		{"valid", func(*PostInput) {}, false},
		{"metadata object", func(in *PostInput) { in.Metadata = jsontext.Value(`{"order":"42"}`) }, false},
		{"missing key", func(in *PostInput) { in.IdempotencyKey = "" }, true},
		{"key too long", func(in *PostInput) { in.IdempotencyKey = strings.Repeat("k", maxIdempotencyKeyLen+1) }, true},
		{"key with NUL", func(in *PostInput) { in.IdempotencyKey = "k\x00" }, true},
		{"description invalid utf8", func(in *PostInput) { in.Description = "\xc3\x28" }, true},
		{"description too long", func(in *PostInput) { in.Description = strings.Repeat("d", maxDescriptionLen+1) }, true},
		{"metadata array", func(in *PostInput) { in.Metadata = jsontext.Value(`[1]`) }, true},
		{"metadata null", func(in *PostInput) { in.Metadata = jsontext.Value(`null`) }, true},
		{"metadata with NUL", func(in *PostInput) { in.Metadata = jsontext.Value(`{"a":"\u0000"}`) }, true},
		{"metadata too large", func(in *PostInput) {
			in.Metadata = jsontext.Value(`{"a":"` + strings.Repeat("x", maxMetadataBytes) + `"}`)
		}, true},
		{"single posting", func(in *PostInput) { in.Postings = in.Postings[:1] }, true},
		{"too many postings", func(in *PostInput) { in.Postings = make([]Posting, maxPostings+1) }, true},
		{"nil account", func(in *PostInput) { in.Postings[0].AccountID = uuid.Nil }, true},
		{"bad side", func(in *PostInput) { in.Postings[1].Side = "both" }, true},
		{"zero amount", func(in *PostInput) { in.Postings[0].Amount = amt(0) }, true},
		{"negative amount", func(in *PostInput) { in.Postings[0].Amount = amt(-100) }, true},
		{"bad currency", func(in *PostInput) { in.Postings[0].Currency = "dollars" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid()
			tt.mutate(&in)
			err := validatePost(in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validatePost() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateHoldCaptureSchedule(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	tests := []struct {
		name string
		err  error
	}{
		{"hold valid", validateHold(CreateHoldInput{IdempotencyKey: "h", AccountID: uuid.New(), Amount: amt(1), ExpiresAt: future})},
		{"capture valid", validateCapture(CaptureInput{IdempotencyKey: "c", Destination: uuid.New(), Amount: amt(1)})},
		{"schedule valid", validateSchedule(ScheduleInput{ExecuteAt: future,
			IdempotencyKey: "s",
			Postings:       []Posting{{AccountID: uuid.New(), Side: Debit, Amount: amt(1)}, {AccountID: uuid.New(), Side: Credit, Amount: amt(1)}}})},
	}
	for _, tt := range tests {
		if tt.err != nil {
			t.Errorf("%s: unexpected error %v", tt.name, tt.err)
		}
	}

	invalid := map[string]error{
		"hold missing account": validateHold(CreateHoldInput{IdempotencyKey: "h", Amount: amt(1), ExpiresAt: future}),
		"hold zero amount":     validateHold(CreateHoldInput{IdempotencyKey: "h", AccountID: uuid.New(), ExpiresAt: future}),
		"hold no expiry":       validateHold(CreateHoldInput{IdempotencyKey: "h", AccountID: uuid.New(), Amount: amt(1)}),
		"hold bad currency":    validateHold(CreateHoldInput{IdempotencyKey: "h", AccountID: uuid.New(), Amount: amt(1), Currency: "x", ExpiresAt: future}),
		"capture no dest":      validateCapture(CaptureInput{IdempotencyKey: "c", Amount: amt(1)}),
		"capture zero":         validateCapture(CaptureInput{IdempotencyKey: "c", Destination: uuid.New()}),
		"capture no key":       validateCapture(CaptureInput{Destination: uuid.New(), Amount: amt(1)}),
		"schedule no time":     validateSchedule(ScheduleInput{}),
		"reverse no key":       validateReverse(ReverseInput{}),
	}
	for name, err := range invalid {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestTransactionMatches(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	txn := Transaction{
		Description: "coffee",
		Metadata:    jsontext.Value(`{"order": "42", "tip": 1}`),
		Postings: []Posting{
			{AccountID: a, Side: Debit, Amount: amt(450), Currency: "USD"},
			{AccountID: b, Side: Credit, Amount: amt(450), Currency: "USD"},
		},
	}
	base := func() PostInput {
		return PostInput{
			IdempotencyKey: "k",
			Description:    "coffee",
			Metadata:       jsontext.Value(`{"tip":1,"order":"42"}`),
			Postings: []Posting{
				{AccountID: a, Side: Debit, Amount: amt(450)},
				{AccountID: b, Side: Credit, Amount: amt(450)},
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*PostInput)
		want   bool
	}{
		{"identical modulo key order and whitespace", func(*PostInput) {}, true},
		{"explicit matching currency", func(in *PostInput) { in.Postings[0].Currency = "USD" }, true},
		{"different metadata", func(in *PostInput) { in.Metadata = jsontext.Value(`{"order":"43","tip":1}`) }, false},
		{"missing metadata", func(in *PostInput) { in.Metadata = nil }, false},
		{"different currency", func(in *PostInput) { in.Postings[0].Currency = "EUR" }, false},
		{"different description", func(in *PostInput) { in.Description = "tea" }, false},
		{"different amount", func(in *PostInput) { in.Postings[0].Amount = amt(451) }, false},
		{"different side", func(in *PostInput) { in.Postings[0].Side = Credit }, false},
		{"different account", func(in *PostInput) { in.Postings[0].AccountID = uuid.New() }, false},
		{"reordered", func(in *PostInput) { in.Postings[0], in.Postings[1] = in.Postings[1], in.Postings[0] }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			if got := txn.matches(in); got != tt.want {
				t.Fatalf("matches() = %v, want %v", got, tt.want)
			}
		})
	}

	empty := Transaction{Metadata: jsontext.Value(`{}`), Postings: txn.Postings}
	if !empty.matches(PostInput{Postings: base().Postings}) {
		t.Error("empty metadata should match omitted metadata")
	}
}
