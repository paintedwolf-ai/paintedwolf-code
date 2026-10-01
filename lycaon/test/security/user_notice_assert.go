package security

import (
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func assertUserFacingHostError(t *testing.T, host wire.SessionHostError, forbiddenDebug ...string) {
	t.Helper()
	if strings.TrimSpace(string(host.Code)) == "" {
		t.Fatal("expected host_error code on wire")
	}
	if strings.TrimSpace(host.Title) == "" {
		t.Fatalf("expected rendered title for host_error code %q", host.Code)
	}
	if strings.TrimSpace(host.Message) == "" {
		t.Fatalf("expected rendered message for host_error code %q", host.Code)
	}
	blob := strings.Join([]string{host.Title, host.Message, host.SuggestedAction}, "\n")
	if strings.Contains(blob, "{{") || strings.Contains(blob, "{%") {
		t.Fatalf("unrendered template leaked to host_error for %q: %q", host.Code, blob)
	}
	for _, frag := range forbiddenDebug {
		if frag != "" && strings.Contains(host.Message, frag) {
			t.Fatalf("debug message leaked to host_error for %q: %q", host.Code, host.Message)
		}
	}
}
