package oarcopy

import (
	"strings"
	"testing"
)

func TestSocketRecoveryReferencesComeFromObservedPaths(t *testing.T) {
	for _, paths := range []any{[]string{"/tmp/service.sock"}, []any{"/tmp/service.sock", 5, "relative"}} {
		facts := FactsFromData(map[string]any{"tool": "command", "refused_socket_paths": paths, "socket_url_references": "forged"})
		if facts["paintedwolf.socket_path_references"] != "{{paintedwolf-socket:/tmp/service.sock}}" || facts["paintedwolf.socket_url_references"] != "{{paintedwolf-socket:unix-url:/tmp/service.sock}}" {
			t.Fatalf("wrong references: %+v", facts)
		}
	}
	facts := FactsFromData(map[string]any{"rejection_code": "SANDBOX_SOCKET_PERMIT_REQUIRED", "path": "/tmp/retry.sock"})
	if !strings.Contains(facts["paintedwolf.socket_url_references"].(string), "/tmp/retry.sock") {
		t.Fatal("missing permit lost its declared path")
	}
	facts = FactsFromData(map[string]any{"path": "/tmp/ordinary"})
	if facts["paintedwolf.socket_url_references"] != "" {
		t.Fatal("ordinary path became socket recovery")
	}
}
