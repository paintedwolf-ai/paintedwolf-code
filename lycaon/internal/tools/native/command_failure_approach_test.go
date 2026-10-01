package native

import (
	"strings"
	"testing"
)

func TestCommandFailureApproachKeyIncludesConfinement(t *testing.T) {
	base := map[string]any{"command": "false"}
	plain := commandFailureApproachKey(base)
	if !strings.Contains(plain, `"command":"false"`) {
		t.Fatalf("plain key missing command: %s", plain)
	}
	if commandFailureApproachKey(map[string]any{
		"command":     "false",
		"socks_proxy": true,
	}) == plain {
		t.Fatal("socks_proxy must change the approach key")
	}
	if commandFailureApproachKey(map[string]any{
		"command": "false",
		"cwd":     "drafts",
	}) == plain {
		t.Fatal("cwd must change the approach key")
	}
	if commandFailureApproachKey(map[string]any{
		"command":            "false",
		"capability_request": map[string]any{"direct_ip": map[string]any{}},
	}) == plain {
		t.Fatal("capability_request.direct_ip must change the approach key")
	}
	if commandFailureApproachKey(map[string]any{
		"command":            "false",
		"capability_request": map[string]any{"local_listen": map[string]any{}},
	}) == commandFailureApproachKey(map[string]any{
		"command":            "false",
		"capability_request": map[string]any{"direct_ip": map[string]any{}},
	}) {
		t.Fatal("distinct capability fields must not share an approach key")
	}
	if commandFailureApproachKey(map[string]any{"command": "false", "wait_ms": 5000.0}) != plain {
		t.Fatal("wait_ms is not part of the confined approach")
	}
}
