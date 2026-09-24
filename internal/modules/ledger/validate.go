package ledger

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxCodeLen           = 128
	maxNameLen           = 255
	maxIdempotencyKeyLen = 255
	maxDescriptionLen    = 1024
	maxMetadataBytes     = 16 << 10
	maxPostings          = 1000
	maxBatchSize         = 1000
	maxStatementLimit    = 1000
	maxListLimit         = 1000
	maxCurrencyExponent  = 30
	currencyRule         = "currency must be 3-16 characters of A-Z, 0-9 and _, starting with a letter"
)

func validateCurrency(in CreateCurrencyInput) error {
	switch {
	case in.Code.Validate() != nil:
		return fmt.Errorf("%w: %s", ErrInvalid, currencyRule)
	case in.Exponent < 0 || in.Exponent > maxCurrencyExponent:
		return fmt.Errorf("%w: exponent must be 0-%d", ErrInvalid, maxCurrencyExponent)
	}
	return nil
}

func validateAccount(in CreateAccountInput) error {
	if err := validateDetails(in.Name, in.Description, in.Metadata, false); err != nil {
		return err
	}
	switch {
	case in.LedgerID == uuid.Nil:
		return fmt.Errorf("%w: ledger_id is required", ErrInvalid)
	case strings.TrimSpace(in.Code) == "" || len(in.Code) > maxCodeLen:
		return fmt.Errorf("%w: code must be 1-%d characters", ErrInvalid, maxCodeLen)
	case !storableText(in.Code):
		return fmt.Errorf("%w: code must be valid UTF-8 without NUL", ErrInvalid)
	case in.Currency.Validate() != nil:
		return fmt.Errorf("%w: %s", ErrInvalid, currencyRule)
	case !in.NormalSide.valid():
		return fmt.Errorf("%w: normal_side must be debit or credit", ErrInvalid)
	case in.OverdraftLimit.Sign() < 0:
		return fmt.Errorf("%w: overdraft_limit must not be negative", ErrInvalid)
	}
	return nil
}

func validateDetails(name, description string, metadata jsontext.Value, nameRequired bool) error {
	switch {
	case nameRequired && strings.TrimSpace(name) == "":
		return fmt.Errorf("%w: name is required", ErrInvalid)
	case len(name) > maxNameLen:
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalid, maxNameLen)
	case !storableText(name):
		return fmt.Errorf("%w: name must be valid UTF-8 without NUL", ErrInvalid)
	}
	return validateText(description, metadata)
}

func validatePost(in PostInput) error {
	if err := validateKeyAndText(in.IdempotencyKey, in.Description, in.Metadata); err != nil {
		return err
	}
	switch {
	case in.Status != "" && in.Status != TransactionPending && in.Status != TransactionPosted:
		return fmt.Errorf("%w: status must be pending or posted", ErrInvalid)
	case len(in.ExternalID) > maxIdempotencyKeyLen:
		return fmt.Errorf("%w: external_id exceeds %d characters", ErrInvalid, maxIdempotencyKeyLen)
	case !storableText(in.ExternalID):
		return fmt.Errorf("%w: external_id must be valid UTF-8 without NUL", ErrInvalid)
	case in.EffectiveAt != nil && !representableTime(*in.EffectiveAt):
		return fmt.Errorf("%w: effective_at must be between years 1 and 9999", ErrInvalid)
	}
	return validateEntries(in.Postings)
}

func representableTime(t time.Time) bool {
	year := t.UTC().Year()
	return year >= 1 && year <= 9999
}

func validateEntries(postings []Posting) error {
	if len(postings) < 2 || len(postings) > maxPostings {
		return fmt.Errorf("%w: transaction needs 2-%d entries", ErrInvalid, maxPostings)
	}
	for i, p := range postings {
		switch {
		case p.AccountID == uuid.Nil:
			return fmt.Errorf("%w: entry %d has no account_id", ErrInvalid, i)
		case !p.Side.valid():
			return fmt.Errorf("%w: entry %d side must be debit or credit", ErrInvalid, i)
		case p.Amount.Sign() <= 0:
			return fmt.Errorf("%w: entry %d amount must be positive", ErrInvalid, i)
		case p.Currency != "" && p.Currency.Validate() != nil:
			return fmt.Errorf("%w: entry %d %s", ErrInvalid, i, currencyRule)
		case p.LockVersion != nil && *p.LockVersion < 0:
			return fmt.Errorf("%w: entry %d lock_version must not be negative", ErrInvalid, i)
		}
	}
	return nil
}

func validateUpdateTransaction(in UpdateTransactionInput) error {
	if in.Description != nil {
		if err := validateText(*in.Description, nil); err != nil {
			return err
		}
	}
	if len(in.Metadata) > maxMetadataBytes {
		return fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalid, maxMetadataBytes)
	}
	if in.EffectiveAt != nil && !representableTime(*in.EffectiveAt) {
		return fmt.Errorf("%w: effective_at must be between years 1 and 9999", ErrInvalid)
	}
	if in.Postings != nil {
		return validateEntries(in.Postings)
	}
	return nil
}

func validatePartialEntries(postings []Posting) error {
	if len(postings) == 0 {
		return nil
	}
	return validateEntries(postings)
}

func validateReverse(in ReverseInput) error {
	return validateKeyAndText(in.IdempotencyKey, in.Description, in.Metadata)
}

func validateHold(in CreateHoldInput) error {
	if err := validateKeyAndText(in.IdempotencyKey, in.Description, nil); err != nil {
		return err
	}
	switch {
	case in.AccountID == uuid.Nil:
		return fmt.Errorf("%w: account_id is required", ErrInvalid)
	case in.Amount.Sign() <= 0:
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	case in.Currency != "" && in.Currency.Validate() != nil:
		return fmt.Errorf("%w: %s", ErrInvalid, currencyRule)
	case in.ExpiresAt.IsZero():
		return fmt.Errorf("%w: expires_at is required", ErrInvalid)
	}
	return nil
}

func validateCapture(in CaptureInput) error {
	if err := validateKeyAndText(in.IdempotencyKey, in.Description, in.Metadata); err != nil {
		return err
	}
	switch {
	case in.Destination == uuid.Nil:
		return fmt.Errorf("%w: destination_account_id is required", ErrInvalid)
	case in.Amount.Sign() <= 0:
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	return nil
}

func validateSchedule(in ScheduleInput) error {
	if in.ExecuteAt.IsZero() {
		return fmt.Errorf("%w: execute_at is required", ErrInvalid)
	}
	return validatePost(in.PostInput)
}

func validateKeyAndText(key, description string, metadata jsontext.Value) error {
	switch {
	case strings.TrimSpace(key) == "" || len(key) > maxIdempotencyKeyLen:
		return fmt.Errorf("%w: idempotency_key must be 1-%d characters", ErrInvalid, maxIdempotencyKeyLen)
	case !storableText(key):
		return fmt.Errorf("%w: text must be valid UTF-8 without NUL", ErrInvalid)
	}
	return validateText(description, metadata)
}

func validateText(description string, metadata jsontext.Value) error {
	switch {
	case len(description) > maxDescriptionLen:
		return fmt.Errorf("%w: description exceeds %d characters", ErrInvalid, maxDescriptionLen)
	case len(metadata) > maxMetadataBytes:
		return fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalid, maxMetadataBytes)
	case !storableText(description):
		return fmt.Errorf("%w: text must be valid UTF-8 without NUL", ErrInvalid)
	case !utf8.Valid(metadata) || bytes.Contains(metadata, []byte(`\u0000`)):
		return fmt.Errorf("%w: metadata must be valid UTF-8 without NUL", ErrInvalid)
	}
	if len(bytes.TrimSpace(metadata)) > 0 {
		var obj map[string]jsontext.Value
		if err := json.Unmarshal(metadata, &obj); err != nil || obj == nil {
			return fmt.Errorf("%w: metadata must be a JSON object", ErrInvalid)
		}
	}
	return nil
}

func normalizeMetadata(raw jsontext.Value) jsontext.Value {
	if len(bytes.TrimSpace(raw)) == 0 {
		return jsontext.Value(`{}`)
	}
	compact := jsontext.Value(bytes.Clone(raw))
	if err := compact.Compact(); err != nil {
		return raw
	}
	return compact
}

func storableText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func validateListAccounts(in ListAccountsInput) error {
	switch in.Status {
	case "", AccountOpen, AccountFrozen, AccountClosed:
	default:
		return fmt.Errorf("%w: status must be open, frozen or closed", ErrInvalid)
	}
	if in.Currency != "" && in.Currency.Validate() != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, currencyRule)
	}
	if in.Limit < 1 || in.Limit > maxListLimit {
		return fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit)
	}
	return validateMetadataFilter(in.Metadata)
}
