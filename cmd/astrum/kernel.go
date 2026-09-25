package main

import (
	"context"
	"io/fs"
	"net/http"
	"sync"

	"github.com/charmbracelet/log"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/config"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/module"
	"github.com/pandabase/astrum/internal/kernel/ratelimit"
	"github.com/pandabase/astrum/internal/kernel/web"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

type kernel struct {
	pool    *pgxpool.Pool
	log     *log.Logger
	auth    *auth.Service
	idem    *idempotency.Service
	events  *events.Service
	ledger  *ledger.Module
	modules []module.Module
	limiter *ratelimit.Limiter
}

type migrator interface {
	Name() string
	Migrations() fs.FS
}

func newKernel(pool *pgxpool.Pool, base *log.Logger, cfg config.Config, l *log.Logger) (*kernel, error) {
	idem := idempotency.New(pool, base)
	idem.Scope = auth.Scope
	if cfg.WebhookAllowInsecure {
		l.Warn("webhooks may target http:// and private addresses")
	}
	evs := events.NewService(pool, base, events.Config{
		AllowInsecureURLs: cfg.WebhookAllowInsecure,
		Retention:         cfg.EventRetention,
	})
	ledgerModule, err := ledger.New(pool, base, ledger.Config{
		Workers:          cfg.LedgerWorkers,
		MaxBatch:         cfg.LedgerMaxBatch,
		BatchConcurrency: cfg.LedgerBatchConcurrency,
		SealKey:          []byte(cfg.LedgerSealKey),
	})
	if err != nil {
		return nil, err
	}
	return &kernel{
		pool:    pool,
		log:     base,
		auth:    auth.New(pool, base),
		idem:    idem,
		events:  evs,
		ledger:  ledgerModule,
		modules: []module.Module{ledgerModule},
		limiter: ratelimit.New(cfg.RateLimit, cfg.RateLimitBurst),
	}, nil
}

func (k *kernel) migrate(ctx context.Context) error {
	migrators := []migrator{k.auth, k.idem, k.events}
	for _, m := range k.modules {
		migrators = append(migrators, m)
	}
	for _, m := range migrators {
		if err := db.Migrate(ctx, k.pool, k.log, m.Name(), m.Migrations()); err != nil {
			return err
		}
	}
	return nil
}

func (k *kernel) preflight(ctx context.Context, l *log.Logger) error {
	if err := k.ledger.CheckSealKey(ctx); err != nil {
		return err
	}
	admins, err := k.auth.ActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if admins == 0 {
		l.Warn("no active admin API key; create one with: astrum keys create -name <name>")
	}
	return nil
}

func (k *kernel) start(ctx context.Context, l *log.Logger) *sync.WaitGroup {
	var workers sync.WaitGroup
	workers.Go(func() { _ = k.idem.Run(ctx) })
	workers.Go(func() { _ = k.events.Run(ctx) })
	for _, m := range k.modules {
		workers.Go(func() {
			if err := m.Run(ctx); err != nil {
				l.Error("module stopped with error", "module", m.Name(), "err", err)
			}
		})
	}
	return &workers
}

func (k *kernel) handler(cfg config.Config, l *log.Logger) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(k.pool))
	k.auth.Routes(mux)
	k.events.Routes(mux)
	for _, m := range k.modules {
		m.Routes(mux)
		l.Info("module loaded", "module", m.Name())
	}

	handler := httpx.Logging(k.log, k.auth.Middleware([]string{"/healthz"}, k.limiter.Middleware(k.idem.Middleware(mux))))
	if cfg.WebDir == "" {
		return handler, nil
	}
	handler, err := web.Handler(cfg.WebDir, handler)
	if err != nil {
		return nil, err
	}
	l.Info("serving web interface", "dir", cfg.WebDir)
	return handler, nil
}
