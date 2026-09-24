package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/config"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/logger"
	"github.com/pandabase/astrum/internal/kernel/module"
	"github.com/pandabase/astrum/internal/kernel/web"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

const shutdownTimeout = 30 * time.Second

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err == nil {
		if len(os.Args) > 1 && os.Args[1] == "keys" {
			err = runKeys(ctx, cfg, os.Args[2:], os.Stdout)
		} else {
			err = run(ctx, cfg)
		}
	}
	if err != nil {
		log.Error("astrum exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	base, err := logger.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	log.SetDefault(base)
	l := base.WithPrefix("kernel")

	l.Info("starting", "version", version, "addr", cfg.HTTPAddr, "log_level", cfg.LogLevel)

	pool, err := db.Connect(ctx, cfg.DatabaseURL, db.Options{
		MaxConns:              int32(cfg.DBMaxConns),
		AllowUnsafeDurability: cfg.AllowUnsafeDurability,
	})
	if err != nil {
		return err
	}
	abandoned := false
	defer func() {

		if !abandoned {
			pool.Close()
		}
	}()
	if cfg.AllowUnsafeDurability {
		l.Warn("durability checks disabled: acknowledged transactions may be lost on power failure")
	}
	l.Info("database connected", "max_conns", cfg.DBMaxConns)

	authn := auth.New(pool, base)
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
		return err
	}
	modules := []module.Module{ledgerModule}

	if err := db.Migrate(ctx, pool, base, authn.Name(), authn.Migrations()); err != nil {
		return err
	}
	if err := db.Migrate(ctx, pool, base, idem.Name(), idem.Migrations()); err != nil {
		return err
	}
	if err := db.Migrate(ctx, pool, base, evs.Name(), evs.Migrations()); err != nil {
		return err
	}
	for _, m := range modules {
		if err := db.Migrate(ctx, pool, base, m.Name(), m.Migrations()); err != nil {
			return err
		}
	}
	if err := ledgerModule.CheckSealKey(ctx); err != nil {
		return err
	}
	if admins, err := authn.ActiveAdmins(ctx); err != nil {
		return err
	} else if admins == 0 {
		l.Warn("no active admin API key; create one with: astrum keys create -name <name>")
	}

	workCtx, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWork()
	var workers sync.WaitGroup
	workers.Go(func() { _ = idem.Run(workCtx) })
	workers.Go(func() { _ = evs.Run(workCtx) })
	for _, m := range modules {
		workers.Go(func() {
			if err := m.Run(workCtx); err != nil {
				l.Error("module stopped with error", "module", m.Name(), "err", err)
			}
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(pool))
	authn.Routes(mux)
	evs.Routes(mux)
	for _, m := range modules {
		m.Routes(mux)
		l.Info("module loaded", "module", m.Name())
	}

	handler := httpx.Logging(base, authn.Middleware([]string{"/healthz"}, idem.Middleware(mux)))
	if cfg.WebDir != "" {
		if handler, err = web.Handler(cfg.WebDir, handler); err != nil {
			return err
		}
		l.Info("serving web interface", "dir", cfg.WebDir)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		l.Info("listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
		l.Info("shutting down: draining http")
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		l.Error("http shutdown incomplete", "err", err)
	}

	l.Info("shutting down: draining modules")
	stopWork()
	drained := make(chan struct{})
	go func() {
		workers.Wait()
		close(drained)
	}()

	select {
	case <-drained:
	case <-time.After(shutdownTimeout):
		abandoned = true
		return errors.New("shutdown deadline exceeded, abandoned background work")
	}

	l.Info("stopped")
	return serveErr
}

func health(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			log.FromContext(r.Context()).Error("health check failed", "err", err)
			httpx.Error(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, "database unavailable")
			return
		}
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	}
}
