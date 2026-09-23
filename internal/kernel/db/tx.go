package db

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRetry = errors.New("db: transient conflict")

const (
	maxAttempts = 6
	baseBackoff = 5 * time.Millisecond
	maxBackoff  = 250 * time.Millisecond
)

func RunTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	var err error
	for attempt := range maxAttempts {
		err = pgx.BeginFunc(ctx, pool, fn)
		if err == nil || !Retryable(err) {
			return err
		}
		if err := sleep(ctx, backoff(attempt)); err != nil {
			return err
		}
	}
	return err
}

func Retryable(err error) bool {
	if errors.Is(err, ErrRetry) {
		return true
	}
	switch Code(err) {
	case "40001", "40P01":
		return true
	}
	return pgconn.SafeToRetry(err)
}

func Code(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}

func Constraint(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.ConstraintName
	}
	return ""
}

func backoff(attempt int) time.Duration {
	d := min(baseBackoff<<attempt, maxBackoff)
	return d/2 + rand.N(d/2+1)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
