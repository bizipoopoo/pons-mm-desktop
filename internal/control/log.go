package control

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type taskHandler struct {
	service    *Service
	strategyID string
	attrs      []slog.Attr
	groups     []string
}

func newTaskHandler(service *Service, strategyID string) slog.Handler {
	return &taskHandler{service: service, strategyID: strategyID}
}

func (h *taskHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *taskHandler) Handle(_ context.Context, record slog.Record) error {
	parts := []string{record.Message}
	for _, attr := range h.attrs {
		parts = append(parts, attr.Key+"="+fmt.Sprint(attr.Value.Any()))
	}
	record.Attrs(func(attr slog.Attr) bool {
		parts = append(parts, attr.Key+"="+fmt.Sprint(attr.Value.Any()))
		return true
	})
	h.service.appendLog(LogEntry{
		At: time.Now().UTC().Format(time.RFC3339Nano), StrategyID: h.strategyID,
		Level: strings.ToLower(record.Level.String()), Message: strings.Join(parts, " "),
	})
	return nil
}

func (h *taskHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

func (h *taskHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}
