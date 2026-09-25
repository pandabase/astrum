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
	"github.com/pandabase/astrum/internal/kernel/config"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/logger"
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

	pool, err := connect(ctx, cfg, l)
	if err != nil {
		return err
	}
	abandoned := false
	defer func() {
		if !abandoned {
			pool.Close()
		}
	}()

	k, err := newKernel(pool, base, cfg, l)
	if err != nil {
		return err
	}
	if err := k.migrate(ctx); err != nil {
		return err
	}
	if err := k.preflight(ctx, l); err != nil {
		return err
	}

	workCtx, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWork()
	workers := k.start(workCtx, l)

	handler, err := k.handler(cfg, l)
	if err != nil {
		return err
	}
	serveErr := serve(ctx, cfg.HTTPAddr, handler, l)

	l.Info("shutting down: draining modules")
	stopWork()
	if err := drain(workers); err != nil {
		abandoned = true
		return err
	}

	l.Info("stopped")
	return serveErr
}

func connect(ctx context.Context, cfg config.Config, l *log.Logger) (*pgxpool.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DatabaseURL, db.Options{
		MaxConns:              int32(cfg.DBMaxConns),
		AllowUnsafeDurability: cfg.AllowUnsafeDurability,
	})
	if err != nil {
		return nil, err
	}
	if cfg.AllowUnsafeDurability {
		l.Warn("durability checks disabled: acknowledged transactions may be lost on power failure")
	}
	l.Info("database connected", "max_conns", cfg.DBMaxConns)
	return pool, nil
}

func serve(ctx context.Context, addr string, handler http.Handler, l *log.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		l.Info("listening", "addr", addr)
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
	return serveErr
}

func drain(workers *sync.WaitGroup) error {
	drained := make(chan struct{})
	go func() {
		workers.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		return nil
	case <-time.After(shutdownTimeout):
		return errors.New("shutdown deadline exceeded, abandoned background work")
	}
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
