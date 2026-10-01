package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestShortSessionID(t *testing.T) {
	full := "5dbabb9d-6945-465a-9cb3-601acc67b754"
	got := ShortSessionID(full)
	if got == full {
		t.Fatalf("expected truncation, got %q", got)
	}
	if !strings.HasPrefix(got, full[:8]) {
		t.Fatalf("got %q", got)
	}
}

func TestRedactingHandlerTruncatesSessionIDAttr(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	h := NewRedactingHandler(inner)
	logger := slog.New(h)

	full := "5dbabb9d-6945-465a-9cb3-601acc67b754"
	logger.InfoContext(context.Background(), "session activity", "session_id", full)

	out := buf.String()
	if strings.Contains(out, full) {
		t.Fatalf("log leaked full session id: %s", out)
	}
	if !strings.Contains(out, ShortSessionID(full)) {
		t.Fatalf("log missing truncated id: %s", out)
	}
}

func TestRedactStringBearerToken(t *testing.T) {
	in := "Authorization: Bearer secret-token-value"
	got := RedactString(in)
	if strings.Contains(got, "secret-token-value") {
		t.Fatalf("got %q", got)
	}
}

// A secret-named key with no value on its line must not eat the next line.
// Captured tool output carries `NN\t` prefixes, so that token is a line number.
func TestRedactStringDoesNotCrossLineBreaks(t *testing.T) {
	in := "21\t    elif x_api_key:\n22\t        token = x_api_key\n23\t    if not token:\n"
	got := RedactString(in)
	for _, want := range []string{"21\t", "22\t", "23\t"} {
		if !strings.Contains(got, want) {
			t.Fatalf("line prefix %q lost:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != strings.Count(in, "\n") {
		t.Fatalf("line structure changed:\n%s", got)
	}
}

// Narrowing the separator must not narrow what the scrubber covers.
func TestRedactStringStillMasksSameLineValues(t *testing.T) {
	for _, in := range []string{
		"api_key = AKIAQYJK5TXV4NZR7SGB",
		"client_secret: hunter2-hunter2-hunter2",
		"  password:\ts3cr3t-value",
	} {
		got := RedactString(in)
		if !strings.Contains(got, redacted) {
			t.Fatalf("value survived redaction: %q -> %q", in, got)
		}
	}
}

func TestRedactStringMasksNamedCommandFlags(t *testing.T) {
	for _, in := range []string{
		"cargo publish --token secret-token-value",
		"tool --client-secret='quoted secret' --mode release",
		"tool --api_key=secret-value",
	} {
		got := RedactString(in)
		if !strings.Contains(got, redacted) || strings.Contains(got, "secret-token-value") ||
			strings.Contains(got, "quoted secret") || strings.Contains(got, "secret-value") {
			t.Fatalf("command flag survived redaction: %q -> %q", in, got)
		}
	}
	if got := RedactString("tool -p 8872 --mode release"); got != "tool -p 8872 --mode release" {
		t.Fatalf("ambiguous short flag changed: %q", got)
	}
}

func TestRedactingHandlerScrubsSecretFieldAttributes(t *testing.T) {
	const canary = "canary-secret-val-98765"
	for _, fieldName := range SecretFieldNames() {
		for label, attr := range map[string]slog.Attr{
			"top-level": slog.String(fieldName, canary),
			"grouped":   slog.Group("nested", fieldName, canary),
		} {
			var buf bytes.Buffer
			slog.New(NewRedactingHandler(slog.NewTextHandler(&buf, nil))).LogAttrs(context.Background(), slog.LevelInfo, "event", attr)
			out := buf.String()
			if strings.Contains(out, canary) || !strings.Contains(out, redacted) {
				t.Fatalf("%s field %q not redacted: %s", label, fieldName, out)
			}
		}
	}
}

type bearerLogValuer struct{}

func (bearerLogValuer) LogValue() slog.Value { return slog.StringValue("Bearer canary-token-12345") }

func TestRedactingHandlerScrubsResolvedLogValuers(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewRedactingHandler(slog.NewTextHandler(&buf, nil))).Info("event", "header", bearerLogValuer{})
	if out := buf.String(); strings.Contains(out, "canary-token-12345") {
		t.Fatalf("resolved log value leaked a bearer token: %s", out)
	}
}
