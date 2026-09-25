package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/db"
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
	op := s.begin(ctx, "create currency", "code", in.Code, "exponent", in.Exponent)

	if err := validateCurrency(in); err != nil {
		return Currency{}, op.fail(err)
	}
	c, created, err := insertCurrency(ctx, s.pool, in)
	if err != nil {
		return Currency{}, op.fail(err)
	}
	if !created {
		existing, err := selectCurrency(ctx, s.pool, in.Code)
		if err != nil {
			return Currency{}, op.fail(err)
		}
		if existing.Exponent != in.Exponent {
			return Currency{}, op.fail(fmt.Errorf("%w: %s has exponent %d", ErrCurrencyExists, existing.Code, existing.Exponent))
		}
		op.info("currency creation replayed")
		return existing, nil
	}
	op.info("currency created")
	return c, nil
}

func (s *service) currency(ctx context.Context, code money.Currency) (Currency, error) {
	op := s.begin(ctx, "get currency", "code", code)

	c, err := selectCurrency(ctx, s.pool, code)
	if err != nil {
		return Currency{}, op.fail(err)
	}
	return c, nil
}

func (s *service) listCurrencies(ctx context.Context, after money.Currency, limit int) ([]Currency, error) {
	op := s.begin(ctx, "list currencies", "after", after, "limit", limit)

	if limit < 1 || limit > maxListLimit {
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	}
	currencies, err := selectCurrencies(ctx, s.pool, after, limit)
	if err != nil {
		return nil, op.fail(err)
	}
	return currencies, nil
}

func (s *service) createAccount(ctx context.Context, in CreateAccountInput) (Account, error) {
	op := s.begin(ctx, "create account", "code", in.Code, "currency", in.Currency)

	if err := validateAccount(in); err != nil {
		return Account{}, op.fail(err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Account{}, op.fail(err)
	}
	var (
		acc     Account
		created bool
	)
	err = db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		acc, created, err = insertAccount(ctx, tx, id, in)
		if err != nil || !created {
			return err
		}
		return emit(ctx, tx, eventAccountCreated, toAccount, acc)
	})
	if err != nil {
		return Account{}, op.fail(err)
	}
	if !created {
		existing, err := selectAccountByCode(ctx, s.pool, in.LedgerID, in.Code)
		if err != nil {
			return Account{}, op.fail(err)
		}
		if !existing.sameTerms(in) {
			return Account{}, op.fail(ErrAccountExists)
		}
		op.info("account creation replayed", "account_id", existing.ID)
		return existing, nil
	}

	op.info("account created",
		"account_id", acc.ID,
		"normal_side", acc.NormalSide,
		"allow_negative", acc.AllowNegative,
		"overdraft_limit", acc.OverdraftLimit)
	return acc, nil
}

func (s *service) setAccountStatus(ctx context.Context, id uuid.UUID, target AccountStatus) (Account, error) {
	name := map[AccountStatus]string{AccountOpen: "unfreeze account", AccountFrozen: "freeze account", AccountClosed: "close account"}[target]
	op := s.begin(ctx, name, "account_id", id)

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
		return Account{}, op.fail(err)
	}

	if !changed {
		op.info("account status unchanged", "status", acc.Status)
		return acc, nil
	}
	op.info("account status changed", "from", from, "to", acc.Status)
	return acc, nil
}

func (s *service) updateAccount(ctx context.Context, id uuid.UUID, in UpdateInput) (Account, error) {
	op := s.begin(ctx, "update account", "account_id", id)

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
		next.Name, next.Description, next.Metadata, err = applyUpdate(current.Name, current.Description, current.Metadata, in, false)
		if err != nil {
			return err
		}
		changed = !sameDetails(current.Name, current.Description, current.Metadata, next.Name, next.Description, next.Metadata)
		if !changed {
			acc = current
			return nil
		}
		if acc, err = updateAccountDetails(ctx, tx, next); err != nil {
			return err
		}
		return emit(ctx, tx, eventAccountUpdated, toAccount, acc)
	})
	if err != nil {
		return Account{}, op.fail(err)
	}
	op.info("account updated", "changed", changed, "version", acc.Version)
	return acc, nil
}

func (s *service) account(ctx context.Context, id uuid.UUID) (Account, error) {
	op := s.begin(ctx, "get account", "account_id", id)

	acc, err := selectAccount(ctx, s.pool, id)
	if err != nil {
		return Account{}, op.fail(err)
	}
	op.debug("account loaded", "posted", acc.Posted.Amount, "available", acc.Available.Amount, "version", acc.Version)
	return acc, nil
}

func (s *service) accountEntries(ctx context.Context, id uuid.UUID, after int64, limit int) ([]StatementLine, error) {
	op := s.begin(ctx, "account entries", "account_id", id, "after", after, "limit", limit)

	if limit < 1 || limit > maxStatementLimit || after < 0 {
		err := fmt.Errorf("%w: limit must be 1-%d and after non-negative", ErrInvalid, maxStatementLimit)
		return nil, op.fail(err)
	}
	acc, err := selectAccount(ctx, s.pool, id)
	if err != nil {
		return nil, op.fail(err)
	}
	lines, err := selectAccountEntries(ctx, s.pool, acc, after, limit)
	if err != nil {
		return nil, op.fail(err)
	}
	op.debug("account entries loaded", "lines", len(lines))
	return lines, nil
}

func (s *service) post(ctx context.Context, in PostInput) (Transaction, error) {
	op := s.begin(ctx, "post transaction", "idempotency_key", in.IdempotencyKey)

	if err := validatePost(in); err != nil {
		return Transaction{}, op.fail(err)
	}

	o, err := s.batcher.submit(ctx, &postingRequest{in: in})
	if err == nil {
		err = o.err
	}
	if err != nil {
		return Transaction{}, op.fail(err)
	}

	s.logPosted(op, o)
	return o.txn, nil
}

func (s *service) runBatch(ctx context.Context, reqs []*postingRequest, atomic bool) ([]postingResult, error) {
	select {
	case s.batchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.batchSlots }()
	return runRequests(ctx, s, reqs, atomic)
}

func (s *service) postBatch(ctx context.Context, ins []PostInput, atomic bool) ([]BatchResult, error) {
	op := s.begin(ctx, "post batch", "size", len(ins), "atomic", atomic)

	if len(ins) == 0 || len(ins) > maxBatchSize {
		err := fmt.Errorf("%w: batch needs 1-%d transactions", ErrInvalid, maxBatchSize)
		return nil, op.fail(err)
	}

	results := make([]BatchResult, len(ins))
	reqs := make([]*postingRequest, 0, len(ins))
	positions := make([]int, 0, len(ins))
	for i, in := range ins {
		if err := validatePost(in); err != nil {
			if atomic {
				return nil, op.fail(fmt.Errorf("transaction %d: %w", i, err))
			}
			results[i] = failedResult(err)
			continue
		}
		reqs = append(reqs, &postingRequest{in: in})
		positions = append(positions, i)
	}

	if len(reqs) > 0 {
		posted, err := s.runBatch(ctx, reqs, atomic)
		if err != nil {
			return nil, op.fail(err)
		}
		for j, o := range posted {
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
	op.logAt(level, "batch processed", "succeeded", len(results)-failed, "failed", failed)
	return results, nil
}

func (s *service) transaction(ctx context.Context, id uuid.UUID) (Transaction, error) {
	op := s.begin(ctx, "get transaction", "transaction_id", id)

	txn, err := selectTransaction(ctx, s.pool, id)
	if err != nil {
		return Transaction{}, op.fail(err)
	}
	op.debug("transaction loaded", "postings", len(txn.Postings))
	return txn, nil
}

func (s *service) listAccounts(ctx context.Context, in ListAccountsInput) ([]Account, error) {
	op := s.begin(ctx, "list accounts", "status", in.Status, "currency", in.Currency, "limit", in.Limit)

	if err := validateListAccounts(in); err != nil {
		return nil, op.fail(err)
	}
	accounts, err := selectAccounts(ctx, s.pool, in)
	if err != nil {
		return nil, op.fail(err)
	}
	op.debug("accounts listed", "count", len(accounts))
	return accounts, nil
}

func (s *service) listTransactions(ctx context.Context, in ListTransactionsInput) ([]Transaction, error) {
	op := s.begin(ctx, "list transactions", "account_id", in.AccountID, "limit", in.Limit)

	switch {
	case in.Limit < 1 || in.Limit > maxListLimit:
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	case in.Status != "" && in.Status != TransactionPending && in.Status != TransactionPosted && in.Status != TransactionArchived:
		return nil, op.fail(fmt.Errorf("%w: status must be pending, posted or archived", ErrInvalid))
	}
	if err := validateMetadataFilter(in.Metadata); err != nil {
		return nil, op.fail(err)
	}
	if err := validateRange(in.Effective); err != nil {
		return nil, op.fail(err)
	}
	txns, err := selectTransactions(ctx, s.pool, in)
	if err != nil {
		return nil, op.fail(err)
	}
	op.debug("transactions listed", "count", len(txns))
	return txns, nil
}

func (s *service) reverse(ctx context.Context, id uuid.UUID, in ReverseInput) (Transaction, error) {
	op := s.begin(ctx, "reverse transaction", "transaction_id", id, "idempotency_key", in.IdempotencyKey)

	if err := validateReverse(in); err != nil {
		return Transaction{}, op.fail(err)
	}

	var o postingResult
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		original, err := lockTransaction(ctx, tx, id)
		if err != nil {
			return err
		}
		if original.Status != TransactionPosted {
			return fmt.Errorf("%w: transaction %s is %s", ErrNotPosted, id, original.Status)
		}
		req := reversalRequest(original, in)

		prior, err := selectReversal(ctx, tx, id)
		switch {
		case err == nil && prior.IdempotencyKey == in.IdempotencyKey && prior.matches(req.in):
			o = postingResult{txn: prior, replayed: true}
			return nil
		case err == nil:
			return ErrAlreadyReversed
		case !errors.Is(err, ErrNotFound):
			return err
		}

		results, err := applyRequests(ctx, tx, []*postingRequest{req}, true)
		if err != nil && !errors.Is(err, errAborted) {
			return err
		}
		o = results[0]
		if o.replayed {
			if prior, err := selectReversal(ctx, tx, id); err != nil || prior.ID != o.txn.ID {
				return ErrIdempotencyConflict
			}
		}
		return o.err
	})
	if err != nil {
		return Transaction{}, op.fail(err)
	}

	s.logPosted(op, o)
	return o.txn, nil
}

func reversalRequest(original Transaction, in ReverseInput) *postingRequest {
	postings := make([]Posting, len(original.Postings))
	for i, p := range original.Postings {
		postings[i] = Posting{AccountID: p.AccountID, Side: p.Side.opposite(), Amount: p.Amount, Currency: p.Currency}
	}
	return &postingRequest{
		in: PostInput{
			IdempotencyKey: in.IdempotencyKey,
			Description:    in.Description,
			Metadata:       in.Metadata,
			Postings:       postings,
		},
		reverses: &original.ID,
	}
}

func (s *service) logPosted(op *operation, o postingResult) {
	if o.replayed {
		op.info("transaction replayed", "transaction_id", o.txn.ID)
		return
	}
	op.info("transaction posted",
		"transaction_id", o.txn.ID,
		"postings", len(o.txn.Postings))
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
	ErrPeriodClosed,
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
	case constraintPeriodOpen:
		return ErrPeriodClosed
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
