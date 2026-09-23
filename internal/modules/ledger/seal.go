package ledger

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/money"
)

const (
	minSealKeyLen   = 32
	sealEncodingV1  = 1
	sealEncodingV2  = 2
	sealEncodingV3  = 3
	sealLockID      = 7_341_902_119
	verifyPageSize  = 1000
	genesisSequence = 0
	maxSealLag      = time.Minute
)

var (
	errSealKey         = fmt.Errorf("ledger: seal key must be at least %d bytes", minSealKeyLen)
	errSealKeyMismatch = errors.New("ledger: seal key does not match the existing chain head")
	errChainRewound    = errors.New("ledger: seal chain head went backwards")
)

type sealer struct {
	key     []byte
	highest atomic.Int64
}

func (s *sealer) link(prev []byte, seq int64, entry []byte) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(prev)
	_ = binary.Write(mac, binary.BigEndian, seq)
	mac.Write(entry)
	return mac.Sum(nil)
}

func entryHash(t Transaction) []byte {
	version := byte(sealEncodingV1)
	for _, p := range t.Postings {
		_, amountFits := p.Amount.Int64()
		_, balanceFits := p.balanceAfter.Int64()
		if !amountFits || !balanceFits {
			version = sealEncodingV2
			break
		}
	}
	writeAmount := func(h hash.Hash, a money.Amount) {
		if version == sealEncodingV1 {
			n, _ := a.Int64()
			_ = binary.Write(h, binary.BigEndian, n)
			return
		}
		b, _ := a.AppendBinary(nil)
		h.Write(b)
	}

	h := sha256.New()
	h.Write([]byte{version})
	h.Write(t.ID[:])
	writeBytes(h, []byte(t.IdempotencyKey))
	writeBytes(h, []byte(t.Description))
	writeBytes(h, t.Metadata)
	if t.ReversesID != nil {
		h.Write([]byte{1})
		h.Write(t.ReversesID[:])
	} else {
		h.Write([]byte{0})
	}
	_ = binary.Write(h, binary.BigEndian, t.CreatedAt.UnixMicro())
	_ = binary.Write(h, binary.BigEndian, uint32(len(t.Postings)))
	for _, p := range t.Postings {
		h.Write(p.AccountID[:])
		writeBytes(h, []byte(p.Currency))
		writeBytes(h, []byte(p.Side))
		writeAmount(h, p.Amount)
		writeAmount(h, p.balanceAfter)
	}
	return h.Sum(nil)
}

func entryHashV3(t Transaction) []byte {
	h := sha256.New()
	h.Write([]byte{sealEncodingV3})
	h.Write(t.ID[:])
	h.Write(t.LedgerID[:])
	writeBytes(h, []byte(t.IdempotencyKey))
	writeBytes(h, []byte(t.ExternalID))
	writeBytes(h, []byte(t.Description))
	writeBytes(h, t.Metadata)
	if t.ReversesID != nil {
		h.Write([]byte{1})
		h.Write(t.ReversesID[:])
	} else {
		h.Write([]byte{0})
	}
	var postedAt int64
	if t.PostedAt != nil {
		postedAt = t.PostedAt.UnixMicro()
	}
	_ = binary.Write(h, binary.BigEndian, []int64{t.CreatedAt.UnixMicro(), t.EffectiveAt.UnixMicro(), postedAt})
	_ = binary.Write(h, binary.BigEndian, uint32(len(t.Postings)))
	for _, p := range t.Postings {
		h.Write(p.AccountID[:])
		writeBytes(h, []byte(p.Currency))
		writeBytes(h, []byte(p.Side))
		amount, _ := p.Amount.AppendBinary(nil)
		after, _ := p.balanceAfter.AppendBinary(nil)
		h.Write(amount)
		h.Write(after)
	}
	return h.Sum(nil)
}

func sealedHash(t Transaction, encoding *int16) []byte {
	if encoding == nil {
		return entryHash(t)
	}
	return entryHashV3(t)
}

func writeBytes(h hash.Hash, b []byte) {
	_ = binary.Write(h, binary.BigEndian, uint32(len(b)))
	h.Write(b)
}

const unsealedSQL = `
	WITH head AS (
		SELECT t.posted_xid, t.id
		FROM ledger_seals AS s
		JOIN ledger_transactions AS t ON t.id = s.transaction_id
		ORDER BY s.seq DESC
		LIMIT 1
	)
	SELECT t.id
	FROM ledger_transactions AS t
	WHERE t.status = 'posted'
	  AND t.posted_xid < pg_snapshot_xmin(pg_current_snapshot())
	  AND (NOT EXISTS (SELECT 1 FROM head) OR (t.posted_xid, t.id) > (SELECT posted_xid, id FROM head))
	ORDER BY t.posted_xid, t.id
	LIMIT $1`

type chainHead struct {
	seq  int64
	hash []byte
}

func (s *service) seal(ctx context.Context, limit int) (int, chainHead, error) {
	var (
		sealed int
		head   chainHead
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		sealed = 0
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, sealLockID).Scan(&locked); err != nil || !locked {
			return err
		}

		var err error
		if head, err = s.loadVerifiedHead(ctx, tx); err != nil {
			return err
		}
		if highest := s.sealer.highest.Load(); head.seq < highest {
			return fmt.Errorf("%w: seq %d, previously %d", errChainRewound, head.seq, highest)
		}

		rows, err := tx.Query(ctx, unsealedSQL, limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(ids) == 0 {
			return err
		}
		txns, err := selectTransactionsInCommitOrder(ctx, tx, ids)
		if err != nil {
			return err
		}

		var (
			seqs    = make([]int64, len(txns))
			txnIDs  = make([]uuid.UUID, len(txns))
			entries = make([][]byte, len(txns))
			chains  = make([][]byte, len(txns))
		)
		for i, t := range txns {
			head.seq++
			entry := entryHashV3(t)
			head.hash = s.sealer.link(head.hash, head.seq, entry)
			seqs[i], txnIDs[i], entries[i], chains[i] = head.seq, t.ID, entry, head.hash
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger_seals (seq, transaction_id, entry_hash, chain_hash, encoding)
			SELECT *, 3 FROM unnest($1::bigint[], $2::uuid[], $3::bytea[], $4::bytea[])`,
			seqs, txnIDs, entries, chains); err != nil {
			return err
		}
		sealed = len(txns)
		return nil
	})
	if err == nil {
		s.sealer.highest.Store(max(s.sealer.highest.Load(), head.seq))
	}
	return sealed, head, err
}

func (s *service) loadVerifiedHead(ctx context.Context, q querier) (chainHead, error) {
	head := chainHead{seq: genesisSequence, hash: make([]byte, sha256.Size)}
	var entry, prev []byte
	err := q.QueryRow(ctx, `
		SELECT s.seq, s.entry_hash, s.chain_hash, coalesce(p.chain_hash, $1)
		FROM ledger_seals AS s
		LEFT JOIN ledger_seals AS p ON p.seq = s.seq - 1
		ORDER BY s.seq DESC
		LIMIT 1`, head.hash).Scan(&head.seq, &entry, &head.hash, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return chainHead{seq: genesisSequence, hash: make([]byte, sha256.Size)}, nil
	}
	if err != nil {
		return chainHead{}, err
	}
	if !hmac.Equal(s.sealer.link(prev, head.seq, entry), head.hash) {
		return chainHead{}, fmt.Errorf("%w at seq %d", errSealKeyMismatch, head.seq)
	}
	return head, nil
}

func selectTransactionsInCommitOrder(ctx context.Context, q querier, ids []uuid.UUID) ([]Transaction, error) {
	rows, err := q.Query(ctx, transactionSelect+`
		WHERE t.id = ANY($1)
		ORDER BY t.posted_xid, t.id, e.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("select transactions: %w", err)
	}
	txns, err := scanTransactions(rows)
	if err != nil {
		return nil, fmt.Errorf("select transactions: %w", err)
	}
	return txns, nil
}

func (s *service) verifyChain(ctx context.Context, tx pgx.Tx) ([]string, chainHead, error) {
	var (
		issues []string
		prev   = make([]byte, sha256.Size)
		next   = int64(genesisSequence + 1)
	)
	for {
		rows, err := tx.Query(ctx, `
			SELECT seq, transaction_id, entry_hash, chain_hash, encoding
			FROM ledger_seals
			WHERE seq >= $1
			ORDER BY seq
			LIMIT $2`, next, verifyPageSize)
		if err != nil {
			return nil, chainHead{}, err
		}
		type sealRow struct {
			seq      int64
			txnID    uuid.UUID
			entry    []byte
			chain    []byte
			encoding *int16
		}
		var page []sealRow
		var r sealRow
		if _, err := pgx.ForEachRow(rows, []any{&r.seq, &r.txnID, &r.entry, &r.chain, &r.encoding}, func() error {
			page = append(page, sealRow{r.seq, r.txnID, bytes.Clone(r.entry), bytes.Clone(r.chain), r.encoding})
			return nil
		}); err != nil {
			return nil, chainHead{}, err
		}
		if len(page) == 0 {
			break
		}

		ids := make([]uuid.UUID, len(page))
		for i, p := range page {
			ids[i] = p.txnID
		}
		txns, err := selectTransactionsInCommitOrder(ctx, tx, ids)
		if err != nil {
			return nil, chainHead{}, err
		}
		byID := make(map[uuid.UUID]Transaction, len(txns))
		for _, t := range txns {
			byID[t.ID] = t
		}

		for _, p := range page {
			if p.seq != next {
				issues = append(issues, fmt.Sprintf("seal chain: expected seq %d, found %d", next, p.seq))
			}
			t, ok := byID[p.txnID]
			switch {
			case !ok:
				issues = append(issues, fmt.Sprintf("seal %d: transaction %s is missing", p.seq, p.txnID))
			case !hmac.Equal(sealedHash(t, p.encoding), p.entry):
				issues = append(issues, fmt.Sprintf("seal %d: transaction %s content was altered", p.seq, p.txnID))
			}
			if want := s.sealer.link(prev, p.seq, p.entry); !hmac.Equal(want, p.chain) {
				issues = append(issues, fmt.Sprintf("seal %d: chain hash does not verify", p.seq))
			}
			prev, next = p.chain, p.seq+1
		}
	}

	rows, err := tx.Query(ctx, `
		WITH head AS (
			SELECT t.posted_xid, t.id
			FROM ledger_seals AS s
			JOIN ledger_transactions AS t ON t.id = s.transaction_id
			ORDER BY s.seq DESC
			LIMIT 1
		)
		SELECT format('transaction %s is unsealed but precedes the chain head', t.id)
		FROM ledger_transactions AS t, head
		WHERE t.status = 'posted'
		  AND (t.posted_xid, t.id) <= (head.posted_xid, head.id)
		  AND NOT EXISTS (SELECT 1 FROM ledger_seals AS s WHERE s.transaction_id = t.id)
		UNION ALL
		-- A wiped or truncated chain has no head to hide behind, so old unsealed rows are gaps too.
		SELECT format('transaction %s has been unsealed since %s', t.id, t.posted_at)
		FROM ledger_transactions AS t
		WHERE t.status = 'posted'
		  AND t.posted_at < now() - make_interval(secs => $1)
		  AND NOT EXISTS (SELECT 1 FROM ledger_seals AS s WHERE s.transaction_id = t.id)
		  AND NOT EXISTS (SELECT 1 FROM head WHERE (t.posted_xid, t.id) <= (head.posted_xid, head.id))`, maxSealLag.Seconds())
	if err != nil {
		return nil, chainHead{}, err
	}
	unsealed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, chainHead{}, err
	}
	return append(issues, unsealed...), chainHead{seq: next - 1, hash: prev}, nil
}

func (h chainHead) String() string {
	return fmt.Sprintf("%d:%s", h.seq, hex.EncodeToString(h.hash))
}
