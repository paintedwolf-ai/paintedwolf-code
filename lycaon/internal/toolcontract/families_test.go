package toolcontract_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestExpandFamiliesGrantsCompanions(t *testing.T) {
	got := toolcontract.ExpandFamilies([]string{"command", "read"})
	want := map[string]bool{"command": true, "command_output": true, "command_stop": true, "read": true}
	if len(got) != len(want) {
		t.Fatalf("expand = %v", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected %s in %v", name, got)
		}
	}
}

func TestAppendImpliedAddsPageFamily(t *testing.T) {
	got := toolcontract.AppendImplied([]string{"wait"}, toolcontract.ResourcePresence{Pages: true})
	found := map[string]bool{}
	for _, name := range got {
		found[name] = true
	}
	for _, name := range []string{"wait", "page_act", "page_snapshot", "page_close"} {
		if !found[name] {
			t.Fatalf("implied pages missing %s: %v", name, got)
		}
	}
	if found["page_open"] {
		t.Fatalf("live pages must not imply page_open: %v", got)
	}
}

func TestAppendImpliedAddsWaitForLiveCommand(t *testing.T) {
	got := toolcontract.AppendImplied(nil, toolcontract.ResourcePresence{CommandJobs: true})
	found := map[string]bool{}
	for _, name := range got {
		found[name] = true
	}
	for _, name := range []string{"command_output", "command_stop", "wait"} {
		if !found[name] {
			t.Fatalf("implied command controls missing %s: %v", name, got)
		}
	}
	if found["command"] {
		t.Fatalf("live command must not imply command launcher: %v", got)
	}
}

func TestIsCatalogDistinguishesStockFromOpenWorld(t *testing.T) {
	if !toolcontract.IsCatalog("command_stop") {
		t.Fatal("command_stop must be catalog")
	}
	if toolcontract.IsCatalog("mcp_github_create_pr") {
		t.Fatal("MCP names must not be catalog")
	}
}
