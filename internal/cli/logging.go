package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type teeHandler struct {
	handlers []slog.Handler
}

var _ slog.Handler = teeHandler{}

func (t teeHandler) Enabled(ctx context.Context, lv slog.Level) bool {
	return slices.ContainsFunc(t.handlers, func(h slog.Handler) bool { return h.Enabled(ctx, lv) })
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range t.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return teeHandler{handlers: out}
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		out[i] = h.WithGroup(name)
	}
	return teeHandler{handlers: out}
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func newLogger(level, logDir string) (*slog.Logger, func()) {
	lv := parseLevel(level)
	handlers := []slog.Handler{
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}),
	}
	closeFn := func() {}

	if logDir != "" {
		path := filepath.Join(logDir, "run.log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: run log unavailable (%v); logging to stderr only\n", err)
		} else {
			handlers = append(handlers,
				slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
			closeFn = func() { f.Close() }
		}
	}

	if len(handlers) == 1 {
		return slog.New(handlers[0]), closeFn
	}
	return slog.New(teeHandler{handlers: handlers}), closeFn
}

func (app *App) stageLog(stage string) *slog.Logger {
	if app.Log == nil {
		return slog.Default()
	}
	return app.Log.With("stage", stage)
}
