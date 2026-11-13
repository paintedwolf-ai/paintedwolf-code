package toolcontract_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestExpandFamiliesGrantsCompanions(t *testing.T) {
	got := toolcontract.ExpandFamilies([]string{"terminal_read", "read"})
	slices.Sort(got)
	want := []string{"read", "terminal_close", "terminal_read", "terminal_send", "terminal_snapshot"}
	if !slices.Equal(got, want) {
		t.Fatalf("expand = %v, want %v", got, want)
	}
}

// Every live resource's own controls form one family that its launcher stays
// out of, so a launcher on a floor never drags controls onto it.
func TestResourceControlsAreOneFamilyWithoutTheirLauncher(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "load native manifest", err)
	implied := map[string][]string{}
	for _, fact := range cfg.ResourceImpliedFacts() {
		implied[fact] = cfg.ResourceImpliedTools(fact)
	}
	controls := nativemanifest.ExclusiveImplied(implied)
	if len(controls) != len(implied) {
		t.Fatalf("every resource needs its own controls: %v", controls)
	}
	presence := map[string]toolcontract.ResourcePresence{
		nativemanifest.ResourceFactCommandJobs: {CommandJobs: true},
		nativemanifest.ResourceFactTerminals:   {Terminals: true},
		nativemanifest.ResourceFactPages:       {Pages: true},
		nativemanifest.ResourceFactHeldCalls:   {HeldCalls: true},
	}
	for fact, want := range controls {
		live := toolcontract.Implied(presence[fact])
		for _, control := range want {
			got := toolcontract.ExpandFamilies([]string{control})
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("%s: family of %s = %v, want %v", fact, control, got, want)
			}
			if !toolcontract.ResourceControl(control) || !slices.Contains(live, control) {
				t.Fatalf("%s: %s must be a control the live resource implies", fact, control)
			}
		}
	}
	for _, launcher := range []string{"command", "terminal_open", "page_open", "wait"} {
		if got := toolcontract.ExpandFamilies([]string{launcher}); !slices.Equal(got, []string{launcher}) {
			t.Fatalf("%s expands to %v", launcher, got)
		}
		if toolcontract.ResourceControl(launcher) {
			t.Fatalf("%s is not a resource control", launcher)
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
