package ledger

import (
	"fmt"
	"maps"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type accountState struct {
	id              uuid.UUID
	ledgerID        uuid.UUID
	currency        money.Currency
	normalSide      Side
	status          AccountStatus
	allowNegative   bool
	overdraftLimit  money.Amount
	postedDebits    money.Amount
	postedCredits   money.Amount
	pendingDebits   money.Amount
	pendingCredits  money.Amount
	held            money.Amount
	version         int64
	originalVersion int64
}

func (a *accountState) postedNet() (money.Amount, error) {
	return a.postedDebits.Sub(a.postedCredits)
}

func (a *accountState) available() (money.Amount, error) {
	in, out, pendingOut := a.postedDebits, a.postedCredits, a.pendingCredits
	if a.normalSide == Credit {
		in, out, pendingOut = a.postedCredits, a.postedDebits, a.pendingDebits
	}
	out, err := sumAmounts(out, pendingOut, a.held)
	if err != nil {
		return money.Amount{}, err
	}
	return in.Sub(out)
}

func (a accountState) views() (Balances, error) {
	posted, pending, available, err := a.balances()
	return Balances{Pending: pending, Posted: posted, Available: available}, err
}

func (a *accountState) balances() (posted, pending, available Balance, err error) {
	pendingDebits, err := a.postedDebits.Add(a.pendingDebits)
	if err != nil {
		return
	}
	pendingCredits, err := a.postedCredits.Add(a.pendingCredits)
	if err != nil {
		return
	}
	availableDebits, availableCredits := a.postedDebits, a.postedCredits
	if a.normalSide == Debit {
		availableCredits, err = pendingCredits.Add(a.held)
	} else {
		availableDebits, err = pendingDebits.Add(a.held)
	}
	if err != nil {
		return
	}
	if posted, err = a.balance(a.postedDebits, a.postedCredits); err != nil {
		return
	}
	if pending, err = a.balance(pendingDebits, pendingCredits); err != nil {
		return
	}
	available, err = a.balance(availableDebits, availableCredits)
	return
}

func (a *accountState) balance(debits, credits money.Amount) (Balance, error) {
	amount, err := debits.Sub(credits)
	if a.normalSide == Credit {
		amount, err = credits.Sub(debits)
	}
	return Balance{Debits: debits, Credits: credits, Amount: amount}, err
}

func sumAmounts(amounts ...money.Amount) (money.Amount, error) {
	var total money.Amount
	for _, a := range amounts {
		var err error
		if total, err = total.Add(a); err != nil {
			return money.Amount{}, err
		}
	}
	return total, nil
}

func (a *accountState) checkFunds(before *accountState) error {
	after, err := a.available()
	if err != nil {
		return err
	}
	prior, err := before.available()
	if err != nil {
		return err
	}
	if a.allowNegative || after.Cmp(prior) >= 0 {
		return nil
	}
	floor, err := after.Add(a.overdraftLimit)
	if err != nil {
		return err
	}
	if floor.Sign() < 0 {
		return fmt.Errorf("%w: account %s", ErrInsufficientFunds, a.id)
	}
	return nil
}

func (a *accountState) checkOpen() error {
	if a.status != AccountOpen {
		return fmt.Errorf("%w: account %s is %s", ErrAccountNotOpen, a.id, a.status)
	}
	return nil
}

func (a *accountState) dirty() bool {
	return a.version != a.originalVersion
}

type ledgerState struct {
	accounts map[uuid.UUID]*accountState

	original map[uuid.UUID]accountState
	monitors map[uuid.UUID][]BalanceMonitor
}

func newLedgerState(accounts []*accountState, monitors []BalanceMonitor) *ledgerState {
	s := &ledgerState{
		accounts: make(map[uuid.UUID]*accountState, len(accounts)),
		original: make(map[uuid.UUID]accountState, len(accounts)),
		monitors: make(map[uuid.UUID][]BalanceMonitor),
	}
	for _, a := range accounts {
		a.originalVersion = a.version
		s.accounts[a.id] = a
		s.original[a.id] = *a
	}
	for _, m := range monitors {
		s.monitors[m.AccountID] = append(s.monitors[m.AccountID], m)
	}
	return s
}

func (s *ledgerState) crossed() ([]BalanceMonitor, []Balances, error) {
	var (
		fired    []BalanceMonitor
		balances []Balances
	)
	for _, a := range s.dirty() {
		watchers := s.monitors[a.id]
		if len(watchers) == 0 {
			continue
		}
		before, err := s.original[a.id].views()
		if err != nil {
			return nil, nil, err
		}
		after, err := a.views()
		if err != nil {
			return nil, nil, err
		}
		for _, m := range watchers {
			if !m.Condition.holds(before) && m.Condition.holds(after) {
				m.Triggered = true
				fired, balances = append(fired, m), append(balances, after)
			}
		}
	}
	return fired, balances, nil
}

type change struct {
	ledgerID uuid.UUID

	unpend []Posting

	add    []Posting
	status TransactionStatus

	releases map[uuid.UUID]money.Amount
}

func (s *ledgerState) apply(in PostInput, releases map[uuid.UUID]money.Amount) ([]Posting, error) {
	_, postings, err := s.transition(change{add: in.Postings, status: in.status(), releases: releases})
	return postings, err
}

func (s *ledgerState) transition(c change) (uuid.UUID, []Posting, error) {
	scratch := make(map[uuid.UUID]*accountState)
	touch := func(id uuid.UUID) (*accountState, error) {
		if a, ok := scratch[id]; ok {
			return a, nil
		}
		a, ok := s.accounts[id]
		if !ok {
			return nil, fmt.Errorf("%w: account %s", ErrNotFound, id)
		}
		clone := *a
		scratch[id] = &clone
		return &clone, nil
	}

	for _, p := range c.unpend {
		a, err := touch(p.AccountID)
		if err != nil {
			return uuid.Nil, nil, err
		}
		total := &a.pendingDebits
		if p.Side == Credit {
			total = &a.pendingCredits
		}
		if p.Amount.Cmp(*total) > 0 {
			return uuid.Nil, nil, fmt.Errorf("ledger: pending %s %s exceeds the %s pending on account %s", p.Side, p.Amount, *total, a.id)
		}
		if *total, err = total.Sub(p.Amount); err != nil {
			return uuid.Nil, nil, err
		}
	}

	ledgerID, pinned := c.ledgerID, c.ledgerID != uuid.Nil
	postings := make([]Posting, len(c.add))
	totals := make(map[money.Currency]money.Amount)
	for i, p := range c.add {
		a, err := touch(p.AccountID)
		if err != nil {
			return uuid.Nil, nil, err
		}
		if err := a.checkOpen(); err != nil {
			return uuid.Nil, nil, err
		}
		if p.LockVersion != nil && *p.LockVersion != s.accounts[a.id].version {
			return uuid.Nil, nil, fmt.Errorf("%w: entry %d account %s is at version %d, not %d",
				ErrLockVersion, i, a.id, s.accounts[a.id].version, *p.LockVersion)
		}
		if !pinned {
			ledgerID, pinned = a.ledgerID, true
		} else if a.ledgerID != ledgerID {
			return uuid.Nil, nil, fmt.Errorf("%w: entry %d account %s is in ledger %s, the transaction in ledger %s",
				ErrCrossLedger, i, a.id, a.ledgerID, ledgerID)
		}
		if p.Currency != "" && p.Currency != a.currency {
			return uuid.Nil, nil, fmt.Errorf("%w: entry %d currency %s does not match account currency %s",
				ErrInvalid, i, p.Currency, a.currency)
		}
		p.Currency = a.currency

		if totals[a.currency], err = totals[a.currency].Add(p.signedAmount()); err != nil {
			return uuid.Nil, nil, err
		}
		if err := a.record(&p, c.status); err != nil {
			return uuid.Nil, nil, err
		}
		postings[i] = p
	}
	for currency, total := range totals {
		if !total.IsZero() {
			return uuid.Nil, nil, fmt.Errorf("%w: %s debits and credits differ by %s", ErrUnbalanced, currency, total)
		}
	}

	for id, amount := range c.releases {
		a, err := touch(id)
		if err != nil {
			return uuid.Nil, nil, err
		}
		if a.held, err = release(a.held, amount, id); err != nil {
			return uuid.Nil, nil, err
		}
	}

	for i := range postings {
		posted, pending, available, err := scratch[postings[i].AccountID].balances()
		if err != nil {
			return uuid.Nil, nil, err
		}
		result := Balances{Pending: pending, Posted: posted, Available: available}
		if err := postings[i].checkLocks(i, result); err != nil {
			return uuid.Nil, nil, err
		}
		postings[i].Resulting = &result
	}

	if err := s.commit(scratch); err != nil {
		return uuid.Nil, nil, err
	}
	return ledgerID, postings, nil
}

func (a *accountState) record(p *Posting, status TransactionStatus) error {
	var err error
	switch {
	case status == TransactionPending && p.Side == Debit:
		a.pendingDebits, err = a.pendingDebits.Add(p.Amount)
	case status == TransactionPending:
		a.pendingCredits, err = a.pendingCredits.Add(p.Amount)
	case p.Side == Debit:
		a.postedDebits, err = a.postedDebits.Add(p.Amount)
	default:
		a.postedCredits, err = a.postedCredits.Add(p.Amount)
	}
	if err != nil || status == TransactionPending {
		return err
	}
	p.balanceAfter, err = a.postedNet()
	return err
}

func (s *ledgerState) reserve(id uuid.UUID, currency money.Currency, amount money.Amount) error {
	a, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("%w: account %s", ErrNotFound, id)
	}
	if err := a.checkOpen(); err != nil {
		return err
	}
	if currency != "" && currency != a.currency {
		return fmt.Errorf("%w: hold currency %s does not match account currency %s", ErrInvalid, currency, a.currency)
	}
	clone := *a
	var err error
	if clone.held, err = clone.held.Add(amount); err != nil {
		return err
	}
	return s.commit(map[uuid.UUID]*accountState{id: &clone})
}

func (s *ledgerState) release(id uuid.UUID, amount money.Amount) error {
	a, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("%w: account %s", ErrNotFound, id)
	}
	clone := *a
	var err error
	if clone.held, err = release(a.held, amount, id); err != nil {
		return err
	}
	return s.commit(map[uuid.UUID]*accountState{id: &clone})
}

func release(held, amount money.Amount, id uuid.UUID) (money.Amount, error) {
	if amount.Cmp(held) > 0 {
		return money.Amount{}, fmt.Errorf("ledger: release of %s exceeds held %s on account %s", amount, held, id)
	}
	return held.Sub(amount)
}

func (s *ledgerState) commit(scratch map[uuid.UUID]*accountState) error {
	for id, a := range scratch {
		if err := a.checkFunds(s.accounts[id]); err != nil {
			return err
		}
	}
	for _, a := range scratch {
		a.version++
	}
	maps.Copy(s.accounts, scratch)
	return nil
}

func (s *ledgerState) dirty() []*accountState {
	var out []*accountState
	for _, a := range s.accounts {
		if a.dirty() {
			out = append(out, a)
		}
	}
	return out
}
