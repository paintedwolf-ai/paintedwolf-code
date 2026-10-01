package observability

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
)

// shortSessionIDBytes bounds a session id in a log line.
const shortSessionIDBytes = 8

var (
	bearerTokenRE = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9\-._~+/]+=*`)
	pemBlockRE    = regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`)
	secretEnvRE   = buildSecretAssignmentRE()
	secretFlagRE  = buildSecretFlagRE()
)

// buildSecretAssignmentRE matches named values in diagnostic text.
func buildSecretAssignmentRE() *regexp.Regexp {
	alts := make([]string, 0, len(secretFieldNames))
	for _, name := range secretFieldNames {
		// Accept joined, underscored, and dashed field names.
		alts = append(alts, strings.ReplaceAll(regexp.QuoteMeta(name), "_", "[_-]?"))
	}
	// Horizontal whitespace prevents a missing value from consuming the next line.
	horizontal := `[^\S\r\n]*`
	return regexp.MustCompile(`(?i)((?:` + strings.Join(alts, "|") + `)` + horizontal + `[=:]` + horizontal + `)\S+`)
}

// buildSecretFlagRE matches secret-bearing long options.
func buildSecretFlagRE() *regexp.Regexp {
	names := make([]string, 0, len(secretFieldNames))
	for _, name := range secretFieldNames {
		names = append(names, strings.ReplaceAll(regexp.QuoteMeta(name), "_", "[-_]?"))
	}
	// Use ASCII spacing so a missing value cannot consume the next line.
	value := `(?:"[^"]*"|'[^']*'|[^\s]+)`
	return regexp.MustCompile(`(?i)((?:--)[a-z0-9_-]*(?:` + strings.Join(names, "|") + `)[a-z0-9_-]*(?:=|[ \t]+))` + value)
}

const redacted = "[REDACTED]"

// RedactString strips bearer tokens, PEM blocks, and common secret patterns from text.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	s = bearerTokenRE.ReplaceAllString(s, "${1}"+redacted)
	s = pemBlockRE.ReplaceAllString(s, redacted)
	s = secretFlagRE.ReplaceAllString(s, "${1}"+redacted)
	s = secretEnvRE.ReplaceAllString(s, "${1}"+redacted)
	return s
}

// RedactingHandler redacts secret-named attributes and secret patterns in string values.
type RedactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps handler with secret redaction.
func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	var attrs []slog.Attr
	record.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, redactAttr(a))
		return true
	})
	cloned := slog.NewRecord(record.Time, record.Level, RedactString(record.Message), record.PC)
	cloned.AddAttrs(attrs...)
	return h.inner.Handle(ctx, cloned)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, redactAttr(a))
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(out)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if IsSecretFieldName(a.Key) {
		return slog.String(a.Key, redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		s := a.Value.String()
		if a.Key == "session_id" {
			s = ShortSessionID(s)
		} else {
			s = RedactString(s)
		}
		return slog.String(a.Key, s)
	case slog.KindGroup:
		attrs := a.Value.Group()
		out := make([]slog.Attr, 0, len(attrs))
		for _, inner := range attrs {
			out = append(out, redactAttr(inner))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	default:
		return a
	}
}

// ShortSessionID truncates UUID session IDs in logs to reduce capability leakage.
func ShortSessionID(id string) string {
	id = strings.TrimSpace(id)
	return runeclamp.ClampBytes(id, shortSessionIDBytes)
}
