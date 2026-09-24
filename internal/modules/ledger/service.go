package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/logger"
	"github.com/pandabase/astrum/internal/money"
)

type service struct {
	pool    *pgxpool.Pool
	log     *log.Logger
	batcher *batcher
	sealer  *sealer

	batchSlots chan struct{}
}

func (s *service) createCurrency(ctx context.Context, in CreateCurrencyInput) (Currency, error) {
	l := logger.For(ctx, s.log).With("op", "create_currency", "code", in.Code, "exponent", in.Exponent)
	start := time.Now()

	if err := validateCurrency(in); err != nil {
		return Currency{}, s.fail(l, "create currency", err, start)
	}
	c, created, err := insertCurrency(ctx, s.pool, in)
	if err != nil {
		return Currency{}, s.fail(l, "create currency", err, start)
	}
	if !created {
		existing, err := selectCurrency(ctx, s.pool, in.Code)
		if err != nil {
			return Currency{}, s.fail(l, "create currency", err, start)
		}
		if existing.Exponent != in.Exponent {
			return Currency{}, s.fail(l, "create currency",
				fmt.Errorf("%w: %s has exponent %d", ErrCurrencyExists, existing.Code, existing.Exponent), start)
		}
		l.Info("currency creation replayed", "duration", time.Since(start))
		return existing, nil
	}
	l.Info("currency created", "duration", time.Since(start))
	return c, nil
}

func (s *service) currency(ctx context.Context, code money.Currency) (Currency, error) {
	l := logger.For(ctx, s.log).With("op", "get_currency", "code", code)
	start := time.Now()

	c, err := selectCurrency(ctx, s.pool, code)
	if err != nil {
		return Currency{}, s.fail(l, "get currency", err, start)
	}
	return c, nil
}

func (s *service) listCurrencies(ctx context.Context, after money.Currency, limit int) ([]Currency, error) {
	l := logger.For(ctx, s.log).With("op", "list_currencies", "after", after, "limit", limit)
	start := time.Now()

	if limit < 1 || limit > maxListLimit {
		return nil, s.fail(l, "list currencies", fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit), start)
	}
	currencies, err := selectCurrencies(ctx, s.pool, after, limit)
	if err != nil {
		return nil, s.fail(l, "list currencies", err, start)
	}
	return currencies, nil
}

func (s *service) createAccount(ctx context.Context, in CreateAccountInput) (Account, error) {
	l := logger.For(ctx, s.log).With("op", "create_account", "code", in.Code, "currency", in.Currency)
	start := time.Now()

	if err := validateAccount(in); err != nil {
		return Account{}, s.fail(l, "create account", err, start)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Account{}, s.fail(l, "create account", err, start)
	}
	var (
		acc     Account
		created bool
	)
	err = db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if acc, created, err = insertAccount(ctx, tx, id, in); err != nil || !created {
			return err
		}
		return emit(ctx, tx, eventAccountCreated, toAccount, acc)
	})
	if err != nil {
		return Account{}, s.fail(l, "create account", err, start)
	}
	if !created {
		existing, err := selectAccountByCode(ctx, s.pool, in.LedgerID, in.Code)
		if err != nil {
			return Account{}, s.fail(l, "create account", err, start)
		}
		if !existing.sameTerms(in) {
			return Account{}, s.fail(l, "create account", ErrAccountExists, start)
		}
		l.Info("account creation replayed", "account_id", existing.ID, "duration", time.Since(start))
		return existing, nil
	}

	l.Info("account created",
		"account_id", acc.ID,
		"normal_side", acc.NormalSide,
		"allow_negative", acc.AllowNegative,
		"overdraft_limit", acc.OverdraftLimit,
		"duration", time.Since(start))
	return acc, nil
}

func (s *service) setAccountStatus(ctx context.Context, id uuid.UUID, target AccountStatus) (Account, error) {
	op := map[AccountStatus]string{AccountOpen: "unfreeze account", AccountFrozen: "freeze account", AccountClosed: "close account"}[target]
	l := logger.For(ctx, s.log).With("op", strings.ReplaceAll(op, " ", "_"), "account_id", id)
	start := time.Now()

	var (
		acc     Account
		changed bool
		from    AccountStatus
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		changed = false
		current, err := queryAccount(ctx, tx, `a.id = $1 FOR UPDATE OF a`, id)
		if err != nil {
			return err
		}
		from = current.Status
		switch {
		case current.Status == target:
			acc = current
			return nil
		case current.Status == AccountClosed:
			return fmt.Errorf("%w: account %s is closed", ErrAccountNotOpen, id)
		case target == AccountClosed && (!current.Posted.Amount.IsZero() || current.Pending != current.Posted || !current.Held.IsZero()):
			return fmt.Errorf("%w: posted %s, pending %s, held %s",
				ErrAccountNotEmpty, current.Posted.Amount, current.Pending.Amount, current.Held)
		}
		if acc, err = updateAccountStatus(ctx, tx, id, target); err != nil {
			return err
		}
		changed = true
		return emit(ctx, tx, eventAccountUpdated, toAccount, acc)
	})
	if err != nil {
		return Account{}, s.fail(l, op, err, start)
	}

	if !changed {
		l.Info("account status unchanged", "status", acc.Status, "duration", time.Since(start))
		return acc, nil
	}
	l.Info("account status changed", "from", from, "to", acc.Status, "duration", time.Since(start))
	return acc, nil
}

func (s *service) updateAccount(ctx context.Context, id uuid.UUID, in UpdateInput) (Account, error) {
	l := logger.For(ctx, s.log).With("op", "update_account", "account_id", id)
	start := time.Now()

	var (
		acc     Account
		changed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := queryAccount(ctx, tx, `a.id = $1 FOR UPDATE OF a`, id)
		if err != nil {
			return err
		}
		next := current
		if next.Name, next.Description, next.Metadata, err = applyUpdate(current.Name, current.Description, current.Metadata, in, false); err != nil {
			return err
		}
		if changed = !sameDetails(current.Name, current.Description, current.Metadata, next.Name, next.Description, next.Metadata); !changed {
			acc = current
			return nil
		}
		if acc, err = updateAccountDetails(ctx, tx, next); err != nil {
			return err
		}
		return emit(ctx, tx, eventAccountUpdated, toAccount, acc)
	})
	if err != nil {
		return Account{}, s.fail(l, "update account", err, start)
	}
	l.Info("account updated", "changed", changed, "version", acc.Version, "duration", time.Since(start))
	return acc, nil
}

func (s *service) account(ctx context.Context, id uuid.UUID) (Account, error) {
	l := logger.For(ctx, s.log).With("op", "get_account", "account_id", id)
	start := time.Now()

	acc, err := selectAccount(ctx, s.pool, id)
	if err != nil {
		return Account{}, s.fail(l, "get account", err, start)
	}
	l.Debug("account loaded", "posted", acc.Posted.Amount, "available", acc.Available.Amount, "version", acc.Version)
	return acc, nil
}

func (s *service) accountEntries(ctx context.Context, id uuid.UUID, after int64, limit int) ([]StatementLine, error) {
	l := logger.For(ctx, s.log).With("op", "account_entries", "account_id", id, "after", after, "limit", limit)
	start := time.Now()

	if limit < 1 || limit > maxStatementLimit || after < 0 {
		err := fmt.Errorf("%w: limit must be 1-%d and after non-negative", ErrInvalid, maxStatementLimit)
		return nil, s.fail(l, "account entries", err, start)
	}
	acc, err := selectAccount(ctx, s.pool, id)
	if err != nil {
		return nil, s.fail(l, "account entries", err, start)
	}
	lines, err := selectAccountEntries(ctx, s.pool, acc, after, limit)
	if err != nil {
		return nil, s.fail(l, "account entries", err, start)
	}
	l.Debug("account entries loaded", "lines", len(lines), "duration", time.Since(start))
	return lines, nil
}

func (s *service) post(ctx context.Context, in PostInput) (Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "post", "idempotency_key", in.IdempotencyKey)
	start := time.Now()

	if err := validatePost(in); err != nil {
		return Transaction{}, s.fail(l, "post transaction", err, start)
	}

	o, err := s.batcher.submit(ctx, &entry{in: in})
	if err == nil {
		err = o.err
	}
	if err != nil {
		return Transaction{}, s.fail(l, "post transaction", err, start)
	}

	s.logPosted(l, o, start)
	return o.txn, nil
}

func (s *service) runBatch(ctx context.Context, entries []*entry, atomic bool) ([]outcome, error) {
	select {
	case s.batchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.batchSlots }()
	return runEntries(ctx, s, entries, atomic)
}

func (s *service) postBatch(ctx context.Context, ins []PostInput, atomic bool) ([]BatchResult, error) {
	l := logger.For(ctx, s.log).With("op", "post_batch", "size", len(ins), "atomic", atomic)
	start := time.Now()

	if len(ins) == 0 || len(ins) > maxBatchSize {
		err := fmt.Errorf("%w: batch needs 1-%d transactions", ErrInvalid, maxBatchSize)
		return nil, s.fail(l, "post batch", err, start)
	}

	results := make([]BatchResult, len(ins))
	entries := make([]*entry, 0, len(ins))
	positions := make([]int, 0, len(ins))
	for i, in := range ins {
		if err := validatePost(in); err != nil {
			if atomic {
				return nil, s.fail(l, "post batch", fmt.Errorf("transaction %d: %w", i, err), start)
			}
			results[i] = failedResult(err)
			continue
		}
		entries = append(entries, &entry{in: in})
		positions = append(positions, i)
	}

	if len(entries) > 0 {
		outcomes, err := s.runBatch(ctx, entries, atomic)
		if err != nil {
			return nil, s.fail(l, "post batch", err, start)
		}
		for j, o := range outcomes {
			if o.err != nil {
				results[positions[j]] = failedResult(o.err)
				continue
			}
			txn := o.txn
			results[positions[j]] = BatchResult{Transaction: &txn, Replayed: o.replayed}
		}
	}

	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	level := log.InfoLevel
	if failed > 0 {
		level = log.WarnLevel
	}
	l.Log(level, "batch processed", "succeeded", len(results)-failed, "failed", failed, "duration", time.Since(start))
	return results, nil
}

func (s *service) transaction(ctx context.Context, id uuid.UUID) (Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "get_transaction", "transaction_id", id)
	start := time.Now()

	txn, err := selectTransaction(ctx, s.pool, id)
	if err != nil {
		return Transaction{}, s.fail(l, "get transaction", err, start)
	}
	l.Debug("transaction loaded", "postings", len(txn.Postings))
	return txn, nil
}

func (s *service) listAccounts(ctx context.Context, in ListAccountsInput) ([]Account, error) {
	l := logger.For(ctx, s.log).With("op", "list_accounts", "status", in.Status, "currency", in.Currency, "limit", in.Limit)
	start := time.Now()

	if err := validateListAccounts(in); err != nil {
		return nil, s.fail(l, "list accounts", err, start)
	}
	accounts, err := selectAccounts(ctx, s.pool, in)
	if err != nil {
		return nil, s.fail(l, "list accounts", err, start)
	}
	l.Debug("accounts listed", "count", len(accounts), "duration", time.Since(start))
	return accounts, nil
}

func (s *service) listTransactions(ctx context.Context, in ListTransactionsInput) ([]Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "list_transactions", "account_id", in.AccountID, "limit", in.Limit)
	start := time.Now()

	switch {
	case in.Limit < 1 || in.Limit > maxListLimit:
		return nil, s.fail(l, "list transactions", fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit), start)
	case in.Status != "" && in.Status != TransactionPending && in.Status != TransactionPosted && in.Status != TransactionArchived:
		return nil, s.fail(l, "list transactions", fmt.Errorf("%w: status must be pending, posted or archived", ErrInvalid), start)
	}
	if err := validateMetadataFilter(in.Metadata); err != nil {
		return nil, s.fail(l, "list transactions", err, start)
	}
	if err := validateRange(in.Effective); err != nil {
		return nil, s.fail(l, "list transactions", err, start)
	}
	txns, err := selectTransactions(ctx, s.pool, in)
	if err != nil {
		return nil, s.fail(l, "list transactions", err, start)
	}
	l.Debug("transactions listed", "count", len(txns), "duration", time.Since(start))
	return txns, nil
}

func (s *service) reverse(ctx context.Context, id uuid.UUID, in ReverseInput) (Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "reverse", "transaction_id", id, "idempotency_key", in.IdempotencyKey)
	start := time.Now()

	if err := validateReverse(in); err != nil {
		return Transaction{}, s.fail(l, "reverse transaction", err, start)
	}

	var o outcome
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		original, err := lockTransaction(ctx, tx, id)
		if err != nil {
			return err
		}
		if original.Status != TransactionPosted {
			return fmt.Errorf("%w: transaction %s is %s", ErrNotPosted, id, original.Status)
		}
		e := reversalEntry(original, in)

		prior, err := selectReversal(ctx, tx, id)
		switch {
		case err == nil && prior.IdempotencyKey == in.IdempotencyKey && prior.matches(e.in):
			o = outcome{txn: prior, replayed: true}
			return nil
		case err == nil:
			return ErrAlreadyReversed
		case !errors.Is(err, ErrNotFound):
			return err
		}

		outcomes, err := applyEntries(ctx, tx, []*entry{e}, true)
		if err != nil && !errors.Is(err, errAborted) {
			return err
		}
		o = outcomes[0]
		if o.replayed {
			if prior, err := selectReversal(ctx, tx, id); err != nil || prior.ID != o.txn.ID {
				return ErrIdempotencyConflict
			}
		}
		return o.err
	})
	if err != nil {
		return Transaction{}, s.fail(l, "reverse transaction", err, start)
	}

	s.logPosted(l, o, start)
	return o.txn, nil
}

func reversalEntry(original Transaction, in ReverseInput) *entry {
	postings := make([]Posting, len(original.Postings))
	for i, p := range original.Postings {
		postings[i] = Posting{AccountID: p.AccountID, Side: p.Side.opposite(), Amount: p.Amount, Currency: p.Currency}
	}
	return &entry{
		in: PostInput{
			IdempotencyKey: in.IdempotencyKey,
			Description:    in.Description,
			Metadata:       in.Metadata,
			Postings:       postings,
		},
		reverses: &original.ID,
	}
}

func (s *service) logPosted(l *log.Logger, o outcome, start time.Time) {
	if o.replayed {
		l.Info("transaction replayed", "transaction_id", o.txn.ID, "duration", time.Since(start))
		return
	}
	l.Info("transaction posted",
		"transaction_id", o.txn.ID,
		"postings", len(o.txn.Postings),
		"duration", time.Since(start))
}

func (s *service) fail(l *log.Logger, op string, err error, start time.Time) error {
	if domainErr := asDomainError(err); domainErr != nil {
		l.Warn(op+" rejected", "err", err, "duration", time.Since(start))
		return domainErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		l.Warn(op+" abandoned", "err", err, "duration", time.Since(start))
		return err
	}
	l.Error(op+" failed", "err", err, "duration", time.Since(start))
	return fmt.Errorf("ledger: %s: %w", op, err)
}

var domainErrors = []error{
	ErrNotFound,
	ErrAccountExists,
	ErrAccountNotOpen,
	ErrAccountNotEmpty,
	ErrCurrencyExists,
	ErrUnknownCurrency,
	ErrUnknownLedger,
	ErrCrossLedger,
	ErrNotPending,
	ErrNotPosted,
	ErrExternalIDExists,
	ErrBalanceLock,
	ErrLockVersion,
	ErrCategoryCycle,
	ErrCategoryDepth,
	ErrCategoryMismatch,
	ErrInvalid,
	ErrUnbalanced,
	ErrInsufficientFunds,
	ErrIdempotencyConflict,
	ErrAlreadyReversed,
	ErrHoldNotPending,
	ErrScheduleNotPending,
	ErrBatchAborted,
	ErrStopped,
	money.ErrOverflow,
}

func asDomainError(err error) error {
	switch db.Constraint(err) {
	case constraintFunds:
		return ErrInsufficientFunds
	case constraintAccountCurrency:
		return ErrUnknownCurrency
	case constraintAccountLedger, constraintCategoryLedger:
		return ErrUnknownLedger
	case constraintCategoryCurrency:
		return ErrUnknownCurrency
	case constraintMemberAccount, constraintMemberCategory, constraintEdgeParent, constraintEdgeChild:
		return ErrCategoryMismatch
	case constraintSameLedger:
		return ErrCrossLedger
	case constraintNotPending:
		return ErrNotPending
	case constraintAccountNotOpen:
		return ErrAccountNotOpen
	case constraintClosedEmpty:
		return ErrAccountNotEmpty
	case constraintReverses:
		return ErrAlreadyReversed
	case constraintPostingsBalanced:
		return ErrUnbalanced
	}
	if db.Code(err) == "22003" {
		return money.ErrOverflow
	}
	for _, target := range domainErrors {
		if errors.Is(err, target) {
			return err
		}
	}
	return nil
}

func failedResult(err error) BatchResult {
	if domainErr := asDomainError(err); domainErr != nil {
		err = domainErr
	}
	return BatchResult{Error: err.Error(), Err: err}
}

func (a Account) sameTerms(in CreateAccountInput) bool {
	return a.Currency == in.Currency &&
		a.NormalSide == in.NormalSide &&
		a.AllowNegative == in.AllowNegative &&
		a.OverdraftLimit == in.OverdraftLimit
}
