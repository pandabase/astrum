package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

type operation struct {
	name  string
	log   *log.Logger
	start time.Time
}

func (s *service) begin(ctx context.Context, name string, attrs ...any) *operation {
	return &operation{
		name:  name,
		log:   logger.For(ctx, s.log).With(append([]any{"op", strings.ReplaceAll(name, " ", "_")}, attrs...)...),
		start: time.Now(),
	}
}

func (o *operation) withDuration(keyvals []any) []any {
	return append(keyvals, "duration", time.Since(o.start))
}

func (o *operation) debug(msg string, keyvals ...any) {
	o.log.Debug(msg, o.withDuration(keyvals)...)
}

func (o *operation) info(msg string, keyvals ...any) {
	o.log.Info(msg, o.withDuration(keyvals)...)
}

func (o *operation) warn(msg string, keyvals ...any) {
	o.log.Warn(msg, o.withDuration(keyvals)...)
}

func (o *operation) logAt(level log.Level, msg string, keyvals ...any) {
	o.log.Log(level, msg, o.withDuration(keyvals)...)
}

func (o *operation) fail(err error) error {
	if domainErr := asDomainError(err); domainErr != nil {
		o.warn(o.name+" rejected", "err", err)
		return domainErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		o.warn(o.name+" abandoned", "err", err)
		return err
	}
	o.log.Error(o.name+" failed", o.withDuration([]any{"err", err})...)
	return fmt.Errorf("ledger: %s: %w", o.name, err)
}
