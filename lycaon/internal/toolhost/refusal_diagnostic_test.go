package toolhost

import (
	"strings"
	"testing"
)

func assertCommandRefusalDiagnostic(t *testing.T, stderr, command, subject, code string) {
	t.Helper()
	prefix, suffix := command+": "+subject+": ", " ["+code+"]\n"
	if !strings.HasPrefix(stderr, prefix) || !strings.HasSuffix(stderr, suffix) || strings.Count(stderr, "\n") != 1 || strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(stderr, prefix), suffix)) == "" {
		t.Fatalf("missing single-line command refusal %s for %s/%s: %q", code, command, subject, stderr)
	}
}
