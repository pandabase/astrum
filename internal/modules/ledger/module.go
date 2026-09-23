package ledger

import (
	"bytes"
	"context"
	"embed"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/money"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Config struct {
	Workers int

	MaxBatch int

	BatchConcurrency int

	QueueSize int

	CommitTimeout time.Duration

	SweepInterval time.Duration

	SweepBatch int

	SealKey []byte
}

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 8
	}

	if c.MaxBatch <= 0 {
		c.MaxBatch = 256
	}

	if c.BatchConcurrency <= 0 {
		c.BatchConcurrency = 4
	}

	if c.QueueSize <= 0 {
		c.QueueSize = 4096
	}

	if c.CommitTimeout <= 0 {
		c.CommitTimeout = 30 * time.Second
	}

	if c.SweepInterval <= 0 {
		c.SweepInterval = time.Second
	}

	if c.SweepBatch <= 0 {
		c.SweepBatch = 500
	}

	return c
}

type Module struct {
	svc *service
	cfg Config
}

func New(pool *pgxpool.Pool, logger *log.Logger, cfg Config) (*Module, error) {
	if len(cfg.SealKey) < minSealKeyLen {
		return nil, errSealKey
	}
	cfg = cfg.withDefaults()
	svc := &service{
		pool:       pool,
		log:        logger.WithPrefix("ledger"),
		sealer:     &sealer{key: bytes.Clone(cfg.SealKey)},
		batchSlots: make(chan struct{}, cfg.BatchConcurrency),
	}
	svc.batcher = newBatcher(svc, cfg)
	return &Module{svc: svc, cfg: cfg}, nil
}

func (m *Module) Name() string { return "ledger" }

func (m *Module) Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

func (m *Module) Routes(mux *http.ServeMux) {
	h := &handler{svc: m.svc}
	h.routes(mux)
}

func (m *Module) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Go(func() { m.svc.batcher.run(ctx) })
	wg.Go(func() { m.sweep(ctx) })
	wg.Go(func() { m.processBulks(ctx) })
	wg.Wait()
	return nil
}

func (m *Module) sweep(ctx context.Context) {
	l := m.svc.log.WithPrefix("ledger/sweeper")
	l.Info("started", "interval", m.cfg.SweepInterval)
	defer l.Info("stopped")

	ticker := time.NewTicker(m.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		for {
			n, err := m.svc.expireHolds(ctx, m.cfg.SweepBatch)
			if err != nil {
				l.Error("expire holds failed", "err", err)
				break
			}
			if n > 0 {
				l.Info("holds expired", "count", n)
			}
			if n < m.cfg.SweepBatch {
				break
			}
		}

		for {
			executed, failed, err := m.svc.executeDue(ctx, m.cfg.SweepBatch)
			if err != nil {
				l.Error("execute schedules failed", "err", err)
				break
			}
			if executed+failed > 0 {
				l.Info("schedules processed", "executed", executed, "failed", failed)
			}
			if executed+failed < m.cfg.SweepBatch {
				break
			}
		}

		for {
			n, head, err := m.svc.seal(ctx, m.cfg.SweepBatch)
			if err != nil {
				l.Error("seal failed", "err", err)
				break
			}
			if n > 0 {

				l.Info("ledger sealed", "count", n, "head", head)
			}
			if n < m.cfg.SweepBatch {
				break
			}
		}
	}
}

func (m *Module) processBulks(ctx context.Context) {
	l := m.svc.log.WithPrefix("ledger/bulk")
	ticker := time.NewTicker(m.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for {
			found, err := m.svc.processBulk(ctx, m.cfg.MaxBatch)
			if err != nil {
				l.Error("bulk request failed", "err", err)
				break
			}
			if !found {
				break
			}
		}
	}
}

func (m *Module) CheckSealKey(ctx context.Context) error {
	head, err := m.svc.loadVerifiedHead(ctx, m.svc.pool)
	if err != nil {
		return err
	}
	m.svc.sealer.highest.Store(max(m.svc.sealer.highest.Load(), head.seq))
	return nil
}

func (m *Module) CreateLedger(ctx context.Context, in CreateLedgerInput) (Ledger, error) {
	return m.svc.createLedger(ctx, in)
}

func (m *Module) Ledger(ctx context.Context, id uuid.UUID) (Ledger, error) {
	return m.svc.ledger(ctx, id)
}

func (m *Module) ListLedgers(ctx context.Context, in ListLedgersInput) ([]Ledger, error) {
	return m.svc.listLedgers(ctx, in)
}

func (m *Module) UpdateLedger(ctx context.Context, id uuid.UUID, in UpdateInput) (Ledger, error) {
	return m.svc.updateLedger(ctx, id, in)
}

func (m *Module) CreateCategory(ctx context.Context, in CreateCategoryInput) (Category, error) {
	return m.svc.createCategory(ctx, in)
}

func (m *Module) Category(ctx context.Context, id uuid.UUID, r EffectiveRange) (Category, error) {
	return m.svc.category(ctx, id, r)
}

func (m *Module) ListCategories(ctx context.Context, in ListCategoriesInput) ([]Category, error) {
	return m.svc.listCategories(ctx, in)
}

func (m *Module) UpdateCategory(ctx context.Context, id uuid.UUID, in UpdateInput) (Category, error) {
	return m.svc.updateCategory(ctx, id, in)
}

func (m *Module) DeleteCategory(ctx context.Context, id uuid.UUID) error {
	return m.svc.deleteCategory(ctx, id)
}

func (m *Module) AddCategoryAccount(ctx context.Context, categoryID, accountID uuid.UUID) (Category, error) {
	return m.svc.setMember(ctx, categoryID, accountID, true)
}

func (m *Module) RemoveCategoryAccount(ctx context.Context, categoryID, accountID uuid.UUID) (Category, error) {
	return m.svc.setMember(ctx, categoryID, accountID, false)
}

func (m *Module) NestCategory(ctx context.Context, parentID, childID uuid.UUID) (Category, error) {
	return m.svc.setChild(ctx, parentID, childID, true)
}

func (m *Module) UnnestCategory(ctx context.Context, parentID, childID uuid.UUID) (Category, error) {
	return m.svc.setChild(ctx, parentID, childID, false)
}

func (m *Module) CreateBalanceMonitor(ctx context.Context, in CreateBalanceMonitorInput) (BalanceMonitor, error) {
	return m.svc.createMonitor(ctx, in)
}

func (m *Module) BalanceMonitor(ctx context.Context, id uuid.UUID) (BalanceMonitor, error) {
	return m.svc.monitor(ctx, id)
}

func (m *Module) ListBalanceMonitors(ctx context.Context, in ListBalanceMonitorsInput) ([]BalanceMonitor, error) {
	return m.svc.listMonitors(ctx, in)
}

func (m *Module) UpdateBalanceMonitor(ctx context.Context, id uuid.UUID, in UpdateInput) (BalanceMonitor, error) {
	return m.svc.updateMonitor(ctx, id, in)
}

func (m *Module) DeleteBalanceMonitor(ctx context.Context, id uuid.UUID) error {
	return m.svc.deleteMonitor(ctx, id)
}

func (m *Module) CreateBulk(ctx context.Context, in CreateBulkInput) (BulkRequest, error) {
	return m.svc.createBulk(ctx, in)
}

func (m *Module) Bulk(ctx context.Context, id uuid.UUID) (BulkRequest, error) {
	return m.svc.bulk(ctx, id)
}

func (m *Module) BulkResults(ctx context.Context, id uuid.UUID, after, limit int) ([]BulkResult, error) {
	return m.svc.bulkResults(ctx, id, after, limit)
}

func (m *Module) ProcessBulk(ctx context.Context) (bool, error) {
	return m.svc.processBulk(ctx, m.cfg.MaxBatch)
}

func (m *Module) CreateSettlement(ctx context.Context, in CreateSettlementInput) (Settlement, error) {
	return m.svc.createSettlement(ctx, in)
}

func (m *Module) Settlement(ctx context.Context, id uuid.UUID) (Settlement, error) {
	return m.svc.settlement(ctx, id)
}

func (m *Module) ListSettlements(ctx context.Context, in ListSettlementsInput) ([]Settlement, error) {
	return m.svc.listSettlements(ctx, in)
}

func (m *Module) CreateCurrency(ctx context.Context, in CreateCurrencyInput) (Currency, error) {
	return m.svc.createCurrency(ctx, in)
}

func (m *Module) Currency(ctx context.Context, code money.Currency) (Currency, error) {
	return m.svc.currency(ctx, code)
}

func (m *Module) ListCurrencies(ctx context.Context, after money.Currency, limit int) ([]Currency, error) {
	return m.svc.listCurrencies(ctx, after, limit)
}

func (m *Module) CreateAccount(ctx context.Context, in CreateAccountInput) (Account, error) {
	return m.svc.createAccount(ctx, in)
}

func (m *Module) Account(ctx context.Context, id uuid.UUID) (Account, error) {
	return m.svc.account(ctx, id)
}

func (m *Module) UpdateAccount(ctx context.Context, id uuid.UUID, in UpdateInput) (Account, error) {
	return m.svc.updateAccount(ctx, id, in)
}

func (m *Module) ListAccounts(ctx context.Context, in ListAccountsInput) ([]Account, error) {
	return m.svc.listAccounts(ctx, in)
}

func (m *Module) FreezeAccount(ctx context.Context, id uuid.UUID) (Account, error) {
	return m.svc.setAccountStatus(ctx, id, AccountFrozen)
}

func (m *Module) UnfreezeAccount(ctx context.Context, id uuid.UUID) (Account, error) {
	return m.svc.setAccountStatus(ctx, id, AccountOpen)
}

func (m *Module) CloseAccount(ctx context.Context, id uuid.UUID) (Account, error) {
	return m.svc.setAccountStatus(ctx, id, AccountClosed)
}

func (m *Module) AccountEntries(ctx context.Context, id uuid.UUID, after int64, limit int) ([]StatementLine, error) {
	return m.svc.accountEntries(ctx, id, after, limit)
}

func (m *Module) ListEntries(ctx context.Context, in ListEntriesInput) ([]Entry, error) {
	return m.svc.listEntries(ctx, in)
}

func (m *Module) Balances(ctx context.Context, id uuid.UUID, r EffectiveRange) (Balances, error) {
	return m.svc.balancesAt(ctx, id, r)
}

func (m *Module) CreateStatement(ctx context.Context, in CreateStatementInput) (Statement, error) {
	return m.svc.createStatement(ctx, in)
}

func (m *Module) Statement(ctx context.Context, id uuid.UUID) (Statement, error) {
	return m.svc.statement(ctx, id)
}

func (m *Module) ListStatements(ctx context.Context, in ListStatementsInput) ([]Statement, error) {
	return m.svc.listStatements(ctx, in)
}

func (m *Module) Post(ctx context.Context, in PostInput) (Transaction, error) {
	return m.svc.post(ctx, in)
}

func (m *Module) PostBatch(ctx context.Context, ins []PostInput, atomic bool) ([]BatchResult, error) {
	return m.svc.postBatch(ctx, ins, atomic)
}

func (m *Module) Transaction(ctx context.Context, id uuid.UUID) (Transaction, error) {
	return m.svc.transaction(ctx, id)
}

func (m *Module) ListTransactions(ctx context.Context, in ListTransactionsInput) ([]Transaction, error) {
	return m.svc.listTransactions(ctx, in)
}

func (m *Module) UpdateTransaction(ctx context.Context, id uuid.UUID, in UpdateTransactionInput) (Transaction, error) {
	return m.svc.updateTransaction(ctx, id, in)
}

func (m *Module) PostTransaction(ctx context.Context, id uuid.UUID, in PostPendingInput) (Transaction, error) {
	return m.svc.postPending(ctx, id, in)
}

func (m *Module) ArchiveTransaction(ctx context.Context, id uuid.UUID) (Transaction, error) {
	return m.svc.archiveTransaction(ctx, id)
}

func (m *Module) Reverse(ctx context.Context, id uuid.UUID, in ReverseInput) (Transaction, error) {
	return m.svc.reverse(ctx, id, in)
}

func (m *Module) CreateHold(ctx context.Context, in CreateHoldInput) (Hold, error) {
	return m.svc.createHold(ctx, in)
}

func (m *Module) Hold(ctx context.Context, id uuid.UUID) (Hold, error) {
	return m.svc.hold(ctx, id)
}

func (m *Module) ListHolds(ctx context.Context, in ListHoldsInput) ([]Hold, error) {
	return m.svc.listHolds(ctx, in)
}

func (m *Module) CaptureHold(ctx context.Context, id uuid.UUID, in CaptureInput) (Hold, error) {
	return m.svc.captureHold(ctx, id, in)
}

func (m *Module) VoidHold(ctx context.Context, id uuid.UUID) (Hold, error) {
	return m.svc.voidHold(ctx, id)
}

func (m *Module) ExpireHolds(ctx context.Context) (int, error) {
	return m.svc.expireHolds(ctx, m.cfg.SweepBatch)
}

func (m *Module) Schedule(ctx context.Context, in ScheduleInput) (ScheduledTransaction, error) {
	return m.svc.schedule(ctx, in)
}

func (m *Module) Scheduled(ctx context.Context, id uuid.UUID) (ScheduledTransaction, error) {
	return m.svc.scheduled(ctx, id)
}

func (m *Module) ListSchedules(ctx context.Context, in ListSchedulesInput) ([]ScheduledTransaction, error) {
	return m.svc.listSchedules(ctx, in)
}

func (m *Module) CancelSchedule(ctx context.Context, id uuid.UUID) (ScheduledTransaction, error) {
	return m.svc.cancelSchedule(ctx, id)
}

func (m *Module) ExecuteDue(ctx context.Context) (executed, failed int, err error) {
	return m.svc.executeDue(ctx, m.cfg.SweepBatch)
}

func (m *Module) Seal(ctx context.Context) (int, error) {
	n, _, err := m.svc.seal(ctx, m.cfg.SweepBatch)
	return n, err
}

func (m *Module) Verify(ctx context.Context) (VerifyReport, error) {
	return m.svc.verify(ctx)
}
