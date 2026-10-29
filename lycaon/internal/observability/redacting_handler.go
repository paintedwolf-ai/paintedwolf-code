package observability

import (
	"context"
	"fmt"
	"log/slog"
)

// RedactingHandler screens log content at persistence, including bound attributes.
type RedactingHandler struct {
	inner    slog.Handler
	bindings []logBinding
}

type logBinding struct {
	group string
	attrs []slog.Attr
}

func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	handler, safe := h.screen(record)
	return handler.Handle(ctx, safe)
}

// A failed matcher contributes no source text or attributes to the sink.
func (h *RedactingHandler) screen(record slog.Record) (handler slog.Handler, safe slog.Record) {
	handler = h.inner
	safe = slog.NewRecord(record.Time, record.Level, redacted, record.PC)
	defer func() {
		if recover() != nil {
			handler = h.inner
			safe = slog.NewRecord(record.Time, record.Level, "Log content withheld: secret screening failed", record.PC)
		}
	}()
	for _, binding := range h.bindings {
		if binding.group != "" {
			handler = handler.WithGroup(RedactCaptureText(binding.group))
		}
		if len(binding.attrs) > 0 {
			attrs := make([]slog.Attr, 0, len(binding.attrs))
			for _, attr := range binding.attrs {
				attrs = append(attrs, redactAttr(attr))
			}
			handler = handler.WithAttrs(attrs)
		}
	}
	safe = slog.NewRecord(record.Time, record.Level, RedactCaptureText(record.Message), record.PC)
	record.Attrs(func(a slog.Attr) bool { safe.AddAttrs(redactAttr(a)); return true })
	return handler, safe
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.bind(logBinding{attrs: append([]slog.Attr(nil), attrs...)})
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.bind(logBinding{group: name})
}

func (h *RedactingHandler) bind(binding logBinding) *RedactingHandler {
	bindings := append([]logBinding(nil), h.bindings...)
	return &RedactingHandler{inner: h.inner, bindings: append(bindings, binding)}
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if IsSecretFieldName(a.Key) {
		return slog.String(RedactCaptureText(a.Key), redacted)
	}
	key := RedactCaptureText(a.Key)
	switch a.Value.Kind() {
	case slog.KindString:
		value := RedactCaptureText(a.Value.String())
		if a.Key == "session_id" {
			value = ShortSessionID(value)
		}
		return slog.String(key, value)
	case slog.KindGroup:
		attrs := a.Value.Group()
		out := make([]slog.Attr, 0, len(attrs))
		for _, attr := range attrs {
			out = append(out, redactAttr(attr))
		}
		return slog.Attr{Key: key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		return slog.String(key, RedactCaptureText(fmt.Sprint(a.Value.Any())))
	default:
		a.Key = key
		return a
	}
}
