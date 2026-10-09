package evidence

import (
	"context"
	"fmt"
	"log/slog"
)

// Logs is the sanitizer of the process's log output (PRN-11). Commands
// register their secrets with it (AddSecret) as soon as they read them.
var Logs = NewSanitizer(NewAliases())

// LogHandler sanitizes the message and every attribute of a log record
// before passing it on (PRN-11). Attributes of kind Any are logged as their
// sanitized text, so no structured value escapes the sanitizer.
type LogHandler struct {
	next slog.Handler
	s    *Sanitizer
}

// NewLogHandler wraps next with the sanitizer s.
func NewLogHandler(next slog.Handler, s *Sanitizer) *LogHandler {
	return &LogHandler{next: next, s: s}
}

// Enabled implements slog.Handler.
func (h *LogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.s.String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, clean)
}

// WithAttrs implements slog.Handler.
func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.attr(a)
	}
	return &LogHandler{next: h.next.WithAttrs(clean), s: h.s}
}

// WithGroup implements slog.Handler.
func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{next: h.next.WithGroup(name), s: h.s}
}

func (h *LogHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.s.String(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]any, len(group))
		for i, g := range group {
			clean[i] = h.attr(g)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindAny:
		return slog.String(a.Key, h.s.String(fmt.Sprint(v.Any())))
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}
