package logger

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/log"
)

func New(w io.Writer, level, format string) (*log.Logger, error) {
	lvl, err := log.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}

	var formatter log.Formatter

	switch format {
	case "", "text":
		formatter = log.TextFormatter
	case "json":
		formatter = log.JSONFormatter
	case "logfmt":
		formatter = log.LogfmtFormatter
	default:
		return nil, fmt.Errorf("logger: unknown format %q", format)
	}

	return log.NewWithOptions(w, log.Options{
		Level:           lvl,
		Formatter:       formatter,
		ReportTimestamp: true,
		TimeFormat:      time.RFC3339Nano,
	}), nil
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type actorKey struct{}

func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func For(ctx context.Context, base *log.Logger) *log.Logger {
	if id := RequestID(ctx); id != "" {
		base = base.With("request_id", id)
	}
	if actor, _ := ctx.Value(actorKey{}).(string); actor != "" {
		base = base.With("actor", actor)
	}
	return base
}
