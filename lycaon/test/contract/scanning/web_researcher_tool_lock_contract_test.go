package contract

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
	"gopkg.in/yaml.v3"
)

// TestWebResearcherToolLockContract locks the web researcher to retrieval plus
// leg reporting. Consequential tools remain forbidden.
//
// The lock reads the tool profile: the agent names one, and that profile is the
// grant.
func TestWebResearcherToolLockContract(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/web-research/agents/web-researcher.yaml"))
	contractcheck.FailErr(t, "read web-researcher.yaml", err)

	var doc struct {
		ID          string `yaml:"id"`
		ToolProfile string `yaml:"tool_profile"`
	}
	contractcheck.FailErr(t, "unmarshal web-researcher.yaml", yaml.Unmarshal(raw, &doc))
	if doc.ID != "web-researcher" {
		t.Fatalf("id = %q", doc.ID)
	}
	if doc.ToolProfile != "web_research" {
		t.Fatalf("tool_profile = %q", doc.ToolProfile)
	}

	agents := toolfixture.LoadBundledAgentRegistry(t)
	p, err := agents.Get("web-researcher")
	contractcheck.FailErr(t, "Get web-researcher", err)
	if p.ToolProfile != doc.ToolProfile {
		t.Fatalf("registry tool_profile = %q want %q", p.ToolProfile, doc.ToolProfile)
	}

	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	var granted []string
	for _, prof := range profiles {
		if prof.ID != p.ToolProfile {
			continue
		}
		for name, ok := range prof.Tools {
			if ok {
				granted = append(granted, name)
			}
		}
	}
	sort.Strings(granted)
	want := []string{
		"complete_leg", "fetch_url", "pack_board", "recall", "record_finding",
		"request_budget", "request_decision", "scan_query", "skills_read", "wait", "web_search",
	}
	if !reflect.DeepEqual(granted, want) {
		t.Fatalf("web_research profile grants %v want exactly %v", granted, want)
	}

	// The resolved surface can narrow the grant, never widen it.
	exec := toolfixture.ContractToolExecutor(t)
	for _, name := range toolfixture.SortedToolNames(context.Background(), exec, p.ToolProfile) {
		lower := strings.ToLower(strings.TrimSpace(name))
		switch {
		case lower == "write", lower == "edit", lower == "command",
			strings.HasPrefix(lower, "git_"),
			strings.HasPrefix(lower, "delegate_"):
			t.Fatalf("web_research profile resolved tool %q breaks Rule of Two", name)
		}
	}
}
