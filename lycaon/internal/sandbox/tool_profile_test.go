package sandbox

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseToolProfileMergesMultipleWriteScopes(t *testing.T) {
	scopes := PathScopeRegistry{
		"product_write": {
			Write: []string{"src/**"},
		},
		"worker_artifacts": {
			Write: []string{settingsoverlay.Rel("work/{job}/**")},
		},
	}
	raw := []byte(`id: restricted_writer
tools:
  write: true
write_scope: product_write
write_scopes:
  - worker_artifacts
`)
	prof, err := ParseToolProfile(raw, scopes)
	testutil.FailErr(t, "ParseToolProfile failed", err)
	want := []string{"src/**", settingsoverlay.Rel("work/{job}/**")}
	if len(prof.WriteGlobs) != len(want) {
		t.Fatalf("write globs = %v want %v", prof.WriteGlobs, want)
	}
	for i := range want {
		if prof.WriteGlobs[i] != want[i] {
			t.Fatalf("write globs = %v want %v", prof.WriteGlobs, want)
		}
	}
}

func TestParseToolProfileResolvesScopes(t *testing.T) {
	scopes := PathScopeRegistry{
		"coordinator_orchestration": {
			Read:  []string{settingsoverlay.Rel("blueprints/**")},
			Write: []string{settingsoverlay.Rel("blueprints/**")},
		},
	}
	raw := []byte(`id: coordinator
tools:
  read: true
read_scope: coordinator_orchestration
write_scope: coordinator_orchestration
`)
	prof, err := ParseToolProfile(raw, scopes)
	testutil.FailErr(t, "ParseToolProfile failed", err)
	if len(prof.ReadGlobs) != 1 || prof.ReadGlobs[0] != settingsoverlay.DirName()+"/blueprints/**" {
		t.Fatalf("read globs = %v", prof.ReadGlobs)
	}
}

func TestParseToolProfileUnknownScope(t *testing.T) {
	raw := []byte(`id: coordinator
read_scope: missing
`)
	_, err := ParseToolProfile(raw, PathScopeRegistry{})
	if err == nil || !strings.Contains(err.Error(), "unknown path scope") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseToolProfileUnrestrictedWhenNoScope(t *testing.T) {
	raw := []byte(`id: implement
tools:
  write: true
`)
	prof, err := ParseToolProfile(raw, PathScopeRegistry{"x": {Write: []string{"a"}}})
	testutil.FailErr(t, "ParseToolProfile failed", err)
	if len(prof.WriteGlobs) != 0 {
		t.Fatalf("write globs = %v want none", prof.WriteGlobs)
	}
}

func TestParseToolProfileStickyDefault(t *testing.T) {
	raw := []byte(`id: implement
tools:
  read: sticky
  wc: true
  chmod: true
`)
	prof, err := ParseToolProfile(raw, PathScopeRegistry{})
	testutil.FailErr(t, "ParseToolProfile failed", err)
	if !prof.Tools["wc"] || !prof.Tools["chmod"] || !prof.Tools["read"] {
		t.Fatalf("all listed tools must stay allowed: %v", prof.Tools)
	}
	if !prof.DeferredTools["wc"] || !prof.DeferredTools["chmod"] {
		t.Fatalf("non-sticky tools must auto-defer: %v", prof.DeferredTools)
	}
	if prof.ToolDeferred("read") {
		t.Fatal("sticky read must not be deferred")
	}
	if !prof.StickyTools["read"] {
		t.Fatalf("StickyTools = %v", prof.StickyTools)
	}
}

func TestParseToolProfileInjectsRequestTools(t *testing.T) {
	raw := []byte(`id: implement
tools:
  read: sticky
  wc: true
`)
	prof, err := ParseToolProfile(raw, PathScopeRegistry{})
	testutil.FailErr(t, "ParseToolProfile failed", err)
	if !prof.Tools["request_tools"] {
		t.Fatal("request_tools must be injected when tools defer")
	}
	if prof.ToolDeferred("request_tools") {
		t.Fatal("injected request_tools must be sticky")
	}

	allSticky := []byte(`id: coordinator
tools:
  read: sticky
`)
	prof, err = ParseToolProfile(allSticky, PathScopeRegistry{})
	testutil.FailErr(t, "ParseToolProfile failed", err)
	if prof.Tools["request_tools"] {
		t.Fatal("request_tools must not be injected when nothing defers")
	}
}

func TestParseToolProfileRequestToolsCannotDefer(t *testing.T) {
	raw := []byte(`id: implement
tools:
  wc: true
  request_tools: true
`)
	if _, err := ParseToolProfile(raw, PathScopeRegistry{}); err == nil || !strings.Contains(err.Error(), "request_tools") {
		t.Fatalf("deferring request_tools must error, got %v", err)
	}
}

func TestParseToolProfileRejectsFalse(t *testing.T) {
	raw := []byte(`id: implement
tools:
  read: sticky
  wc: false
`)
	_, err := ParseToolProfile(raw, PathScopeRegistry{})
	if err == nil || !strings.Contains(err.Error(), "denied by default") {
		t.Fatalf("false value must be rejected (closed by default), got %v", err)
	}
}

func TestParseToolProfileRejectsUnknownToolMode(t *testing.T) {
	for _, val := range []string{"lazy", "deferred"} {
		raw := []byte("id: implement\ntools:\n  wc: " + val + "\n")
		_, err := ParseToolProfile(raw, PathScopeRegistry{})
		if err == nil || !strings.Contains(err.Error(), "sticky") {
			t.Fatalf("value %q: err = %v", val, err)
		}
	}
}

func TestBoundaryToolDeferred(t *testing.T) {
	b := NewBoundary(Config{}, []ToolProfile{{
		ID:            "implement",
		Tools:         map[string]bool{"wc": true, "read": true},
		StickyTools:   map[string]bool{"read": true},
		DeferredTools: map[string]bool{"wc": true},
	}})
	if !b.ToolDeferred("implement", "wc", ToolAccessProfile) {
		t.Fatal("wc must be deferred")
	}
	if b.ToolDeferred("implement", "read", ToolAccessProfile) {
		t.Fatal("read must not be deferred")
	}
	if b.ToolDeferred("missing", "wc", ToolAccessProfile) {
		t.Fatal("unknown profile must not defer")
	}
}

func TestToolProfileToolDeferredWildcard(t *testing.T) {
	raw := []byte(`id: implement
tools:
  read: sticky
  mcp_github_*: true
  mcp_github_get_me: sticky
`)
	prof, err := ParseToolProfile(raw, PathScopeRegistry{})
	testutil.FailErr(t, "ParseToolProfile failed", err)
	if !prof.ToolDeferred("mcp_github_create_pr") {
		t.Fatal("wildcard entry must auto-defer concrete names")
	}
	if prof.ToolDeferred("mcp_github_get_me") {
		t.Fatal("exact sticky pin must win over a deferred pattern")
	}
	if prof.ToolDeferred("mcp_linear_create_issue") {
		t.Fatal("non-matching prefix must not defer")
	}
	if prof.ToolDeferred("read") {
		t.Fatal("sticky read must not be deferred")
	}
}
