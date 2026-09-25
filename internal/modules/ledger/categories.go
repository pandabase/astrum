package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/money"
)

const maxCategoryDepth = 7

func (s *service) createCategory(ctx context.Context, in CreateCategoryInput) (Category, error) {
	op := s.begin(ctx, "create category", "ledger_id", in.LedgerID, "name", in.Name)

	if err := validateCategory(in); err != nil {
		return Category{}, op.fail(err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Category{}, op.fail(err)
	}
	c, err := scanCategory(s.pool.QueryRow(ctx, `
		INSERT INTO ledger_categories (id, ledger_id, currency, normal_side, name, description, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7::text::jsonb)
		RETURNING `+categoryColumns,
		id, in.LedgerID, string(in.Currency), string(in.NormalSide), in.Name, in.Description,
		string(normalizeMetadata(in.Metadata))))
	if err != nil {
		return Category{}, op.fail(err)
	}
	op.info("category created", "category_id", c.ID)
	return c, nil
}

func (s *service) category(ctx context.Context, id uuid.UUID, r EffectiveRange) (Category, error) {
	op := s.begin(ctx, "get category", "category_id", id)

	if err := validateRange(r); err != nil {
		return Category{}, op.fail(err)
	}
	var c Category
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		if c, err = queryCategory(ctx, tx, id); err != nil {
			return err
		}
		c.Balances, err = rollUp(ctx, tx, c, r)
		return err
	})
	if err != nil {
		return Category{}, op.fail(err)
	}
	return c, nil
}

func (s *service) listCategories(ctx context.Context, in ListCategoriesInput) ([]Category, error) {
	op := s.begin(ctx, "list categories", "limit", in.Limit)

	if in.Limit < 1 || in.Limit > maxListLimit {
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	}
	if err := validateMetadataFilter(in.Metadata); err != nil {
		return nil, op.fail(err)
	}
	var categories []Category
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+categoryColumns+`
			FROM ledger_categories
			WHERE ($1::uuid IS NULL OR id < $1)
			  AND ($2::uuid IS NULL OR ledger_id = $2)
			  AND ($3::uuid IS NULL OR id IN (SELECT child_id FROM ledger_category_edges WHERE parent_id = $3))
			  AND ($4::uuid IS NULL OR id IN (SELECT category_id FROM ledger_category_accounts WHERE account_id = $4))
			  AND ($5::jsonb IS NULL OR metadata @> $5)
			ORDER BY id DESC
			LIMIT $6`,
			nullUUID(in.Before), nullUUID(in.LedgerID), nullUUID(in.ParentID), nullUUID(in.AccountID),
			metadataFilter(in.Metadata), in.Limit)
		if err != nil {
			return err
		}
		if categories, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Category, error) { return scanCategory(row) }); err != nil {
			return err
		}
		for i := range categories {
			if categories[i].Balances, err = rollUp(ctx, tx, categories[i], EffectiveRange{}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, op.fail(err)
	}
	return categories, nil
}

func (s *service) updateCategory(ctx context.Context, id uuid.UUID, in UpdateInput) (Category, error) {
	op := s.begin(ctx, "update category", "category_id", id)

	var c Category
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockCategory(ctx, tx, id)
		if err != nil {
			return err
		}
		name, description, metadata, err := applyUpdate(current.Name, current.Description, current.Metadata, in, true)
		if err != nil {
			return err
		}
		c = current
		if !sameDetails(current.Name, current.Description, current.Metadata, name, description, metadata) {
			c, err = scanCategory(tx.QueryRow(ctx, `
				UPDATE ledger_categories
				SET name = $2, description = $3, metadata = $4::text::jsonb, version = version + 1
				WHERE id = $1
				RETURNING `+categoryColumns, id, name, description, string(metadata)))
			if err != nil {
				return err
			}
		}
		c.Balances, err = rollUp(ctx, tx, c, EffectiveRange{})
		return err
	})
	if err != nil {
		return Category{}, op.fail(err)
	}
	op.info("category updated", "version", c.Version)
	return c, nil
}

func (s *service) deleteCategory(ctx context.Context, id uuid.UUID) error {
	op := s.begin(ctx, "delete category", "category_id", id)

	err := s.changeGraph(ctx, id, func(tx pgx.Tx, c Category) error {
		_, err := tx.Exec(ctx, `DELETE FROM ledger_categories WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return op.fail(err)
	}
	op.info("category deleted")
	return nil
}

func (s *service) setMember(ctx context.Context, categoryID, accountID uuid.UUID, member bool) (Category, error) {
	op := s.begin(ctx, "set category account", "category_id", categoryID, "account_id", accountID, "member", member)

	var c Category
	err := s.changeGraph(ctx, categoryID, func(tx pgx.Tx, current Category) error {
		c = current
		if !member {
			_, err := tx.Exec(ctx, `DELETE FROM ledger_category_accounts WHERE category_id = $1 AND account_id = $2`, categoryID, accountID)
			return err
		}
		acc, err := selectAccount(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if acc.LedgerID != current.LedgerID || acc.Currency != current.Currency {
			return fmt.Errorf("%w: account %s is %s in ledger %s, category is %s in ledger %s",
				ErrCategoryMismatch, acc.ID, acc.Currency, acc.LedgerID, current.Currency, current.LedgerID)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_category_accounts (category_id, account_id, ledger_id, currency)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, categoryID, accountID, current.LedgerID, string(current.Currency))
		return err
	})
	if err != nil {
		return Category{}, op.fail(err)
	}
	op.info("category membership set")
	return s.category(ctx, c.ID, EffectiveRange{})
}

func (s *service) setChild(ctx context.Context, parentID, childID uuid.UUID, nested bool) (Category, error) {
	op := s.begin(ctx, "set category child", "category_id", parentID, "child_id", childID, "nested", nested)

	err := s.changeGraph(ctx, parentID, func(tx pgx.Tx, parent Category) error {
		if !nested {
			_, err := tx.Exec(ctx, `DELETE FROM ledger_category_edges WHERE parent_id = $1 AND child_id = $2`, parentID, childID)
			return err
		}
		child, err := queryCategory(ctx, tx, childID)
		if err != nil {
			return err
		}
		if child.LedgerID != parent.LedgerID || child.Currency != parent.Currency {
			return fmt.Errorf("%w: category %s is %s in ledger %s", ErrCategoryMismatch, child.ID, child.Currency, child.LedgerID)
		}
		var cycle bool
		var above, below int
		err = tx.QueryRow(ctx, `
			WITH RECURSIVE
				up(id, depth) AS (
					SELECT $1::uuid, 1
					UNION ALL
					SELECT e.parent_id, up.depth + 1 FROM ledger_category_edges AS e JOIN up ON e.child_id = up.id
					WHERE up.depth <= $3),
				down(id, depth) AS (
					SELECT $2::uuid, 1
					UNION ALL
					SELECT e.child_id, down.depth + 1 FROM ledger_category_edges AS e JOIN down ON e.parent_id = down.id
					WHERE down.depth <= $3)
			SELECT EXISTS (SELECT 1 FROM down WHERE id = $1), (SELECT max(depth) FROM up), (SELECT max(depth) FROM down)`,
			parentID, childID, maxCategoryDepth).Scan(&cycle, &above, &below)
		switch {
		case err != nil:
			return err
		case cycle:
			return fmt.Errorf("%w: %s is already inside %s", ErrCategoryCycle, parentID, childID)
		case above+below > maxCategoryDepth:
			return fmt.Errorf("%w: nesting would make a chain of %d categories", ErrCategoryDepth, above+below)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_category_edges (parent_id, child_id, ledger_id, currency)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, parentID, childID, parent.LedgerID, string(parent.Currency))
		return err
	})
	if err != nil {
		return Category{}, op.fail(err)
	}
	op.info("category nesting set")
	return s.category(ctx, parentID, EffectiveRange{})
}

func (s *service) changeGraph(ctx context.Context, categoryID uuid.UUID, change func(pgx.Tx, Category) error) error {
	return db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := queryCategory(ctx, tx, categoryID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM ledger_ledgers WHERE id = $1 FOR NO KEY UPDATE`, c.LedgerID); err != nil {
			return err
		}

		if c, err = queryCategory(ctx, tx, categoryID); err != nil {
			return err
		}
		return change(tx, c)
	})
}

const categoryColumns = `id, ledger_id, currency, normal_side, name, description, metadata, version, created_at`

func scanCategory(row pgx.Row) (Category, error) {
	var (
		c                    Category
		currency, normalSide string
		metadata             []byte
	)
	err := row.Scan(&c.ID, &c.LedgerID, &currency, &normalSide, &c.Name, &c.Description, &metadata, &c.Version, &c.CreatedAt)
	if err != nil {
		return Category{}, err
	}
	c.Currency, c.NormalSide, c.Metadata = money.Currency(currency), Side(normalSide), bytes.Clone(metadata)
	return c, nil
}

func queryCategory(ctx context.Context, q querier, id uuid.UUID) (Category, error) {
	return findCategory(q.QueryRow(ctx, `SELECT `+categoryColumns+` FROM ledger_categories WHERE id = $1`, id))
}

func lockCategory(ctx context.Context, q querier, id uuid.UUID) (Category, error) {
	return findCategory(q.QueryRow(ctx, `SELECT `+categoryColumns+` FROM ledger_categories WHERE id = $1 FOR UPDATE`, id))
}

func findCategory(row pgx.Row) (Category, error) {
	c, err := scanCategory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Category{}, ErrNotFound
	}
	return c, err
}

const categoryMembers = `
	WITH RECURSIVE tree(id) AS (
		SELECT %[1]s::uuid
		UNION
		SELECT e.child_id FROM ledger_category_edges AS e JOIN tree ON e.parent_id = tree.id
	)
	SELECT DISTINCT account_id FROM ledger_category_accounts WHERE category_id IN (SELECT id FROM tree)`

func rollUp(ctx context.Context, q querier, c Category, r EffectiveRange) (Balances, error) {
	var query string
	var args []any
	if r.From == nil && r.Until == nil {
		query = `
			SELECT a.normal_side, sum(a.posted_debits), sum(a.posted_credits), sum(a.pending_debits),
			       sum(a.pending_credits), sum(a.held)
			FROM ledger_accounts AS a
			WHERE a.id IN (` + fmt.Sprintf(categoryMembers, "$1") + `)
			GROUP BY a.normal_side`
		args = []any{c.ID}
	} else {
		query = `
			WITH members AS (` + fmt.Sprintf(categoryMembers, "$1") + `)
			SELECT a.normal_side,
			       coalesce(sum(x.amount) FILTER (WHERE x.side = 'debit' AND x.posted), 0),
			       coalesce(sum(x.amount) FILTER (WHERE x.side = 'credit' AND x.posted), 0),
			       coalesce(sum(x.amount) FILTER (WHERE x.side = 'debit' AND NOT x.posted), 0),
			       coalesce(sum(x.amount) FILTER (WHERE x.side = 'credit' AND NOT x.posted), 0),
			       0::numeric
			FROM (
				SELECT p.account_id, p.side, p.amount, true AS posted, t.effective_at
				FROM ledger_postings AS p JOIN ledger_transactions AS t ON t.id = p.transaction_id
				WHERE p.account_id IN (SELECT account_id FROM members)
				UNION ALL
				SELECT e.account_id, e.side, e.amount, false, t.effective_at
				FROM ledger_pending_entries AS e
				JOIN ledger_transactions AS t ON t.id = e.transaction_id AND e.version = t.entries_version
				WHERE t.status = 'pending' AND e.account_id IN (SELECT account_id FROM members)
			) AS x
			JOIN ledger_accounts AS a ON a.id = x.account_id
			WHERE ($2::timestamptz IS NULL OR x.effective_at >= $2) AND ($3::timestamptz IS NULL OR x.effective_at < $3)
			GROUP BY a.normal_side`
		args = []any{c.ID, r.From, r.Until}
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return Balances{}, fmt.Errorf("roll up category: %w", err)
	}

	var sums [3][2]money.Amount
	var (
		a    accountState
		side string
	)
	_, err = pgx.ForEachRow(rows, []any{&side, &a.postedDebits, &a.postedCredits, &a.pendingDebits, &a.pendingCredits, &a.held}, func() error {
		a.normalSide = Side(side)
		posted, pending, available, err := a.balances()
		if err != nil {
			return err
		}
		for i, b := range []Balance{posted, pending, available} {
			if sums[i][0], err = sums[i][0].Add(b.Debits); err != nil {
				return err
			}
			if sums[i][1], err = sums[i][1].Add(b.Credits); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Balances{}, fmt.Errorf("roll up category: %w", err)
	}

	view := &accountState{normalSide: c.NormalSide}
	var b Balances
	if b.Posted, err = view.balance(sums[0][0], sums[0][1]); err != nil {
		return Balances{}, err
	}
	if b.Pending, err = view.balance(sums[1][0], sums[1][1]); err != nil {
		return Balances{}, err
	}
	b.Available, err = view.balance(sums[2][0], sums[2][1])
	return b, err
}

func validateCategory(in CreateCategoryInput) error {
	if err := validateDetails(in.Name, in.Description, in.Metadata, true); err != nil {
		return err
	}
	switch {
	case in.LedgerID == uuid.Nil:
		return fmt.Errorf("%w: ledger_id is required", ErrInvalid)
	case in.Currency.Validate() != nil:
		return fmt.Errorf("%w: %s", ErrInvalid, currencyRule)
	case !in.NormalSide.valid():
		return fmt.Errorf("%w: normal_side must be debit or credit", ErrInvalid)
	}
	return nil
}
