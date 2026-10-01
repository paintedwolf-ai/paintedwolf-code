package definition

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestResolveManifestExtendsPlanHasParameters(t *testing.T) {
	catalog, _, err := LoadPackManifestsForCatalog(nil)
	testutil.FailErr(t, "loadCatalogManifestsWithSources failed", err)
	plan, ok := catalog[ManifestKey("plan", "1.0.0")]
	if !ok {
		t.Fatal("missing plan@1.0.0")
	}
	if _, ok := plan.Parameters["research_depth"]; !ok {
		t.Fatal("plan must declare research_depth parameter")
	}
	if _, ok := plan.Parameters["auto_approve"]; !ok {
		t.Fatal("plan must declare auto_approve parameter")
	}
}

func TestResolveManifestExtendsDepthExceeded(t *testing.T) {
	catalog := map[string]Manifest{
		"a@1.0.0": {ID: "a", Version: "1.0.0", Extends: "b@1.0.0"},
		"b@1.0.0": {ID: "b", Version: "1.0.0", Extends: "c@1.0.0"},
		"c@1.0.0": {ID: "c", Version: "1.0.0", Extends: "d@1.0.0"},
		"d@1.0.0": {ID: "d", Version: "1.0.0", Extends: "e@1.0.0"},
		"e@1.0.0": {ID: "e", Version: "1.0.0"},
	}
	if _, err := ResolveManifestChain(catalog["a@1.0.0"], catalog); err == nil {
		t.Fatal("expected depth error")
	}
}

func TestResolveManifestExtendsCycle(t *testing.T) {
	catalog := map[string]Manifest{
		"a@1.0.0": {ID: "a", Version: "1.0.0", Extends: "b@1.0.0"},
		"b@1.0.0": {ID: "b", Version: "1.0.0", Extends: "a@1.0.0"},
	}
	_, err := ResolveManifestChain(catalog["a@1.0.0"], catalog)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergePhaseDefsChildOverrides(t *testing.T) {
	parent := []PhaseDef{
		{ID: "stub", CompleteWhen: "plan_stub_valid", Next: "research"},
		{ID: "research", CompleteWhen: "research_satisfied", Next: "approve"},
	}
	child := []PhaseDef{
		{ID: "stub", Next: "approve"},
	}
	merged := mergePhaseDefs(parent, child)
	if merged[0].Next != "approve" {
		t.Fatalf("stub.next = %q", merged[0].Next)
	}
	if merged[0].CompleteWhen != "plan_stub_valid" {
		t.Fatalf("stub.complete_when = %q", merged[0].CompleteWhen)
	}
}

func TestResolveManifestExtendsIsolatesParentAndSiblings(t *testing.T) {
	parentKey := ManifestKey("base", "1.0.0")
	catalog := map[string]Manifest{
		parentKey: {
			ID: "base", Version: "1.0.0",
			Request:    &ManifestRequest{Cadence: RequestCadenceOnce, Question: "What should this workflow do?"},
			Parameters: map[string]WorkflowParameter{"depth": {Type: "depth", Default: "light"}},
		},
		ManifestKey("left", "1.0.0"): {
			ID: "left", Version: "1.0.0", Extends: parentKey,
			Parameters: map[string]WorkflowParameter{"depth": {Type: "depth", Default: "thorough"}},
		},
		ManifestKey("right", "1.0.0"): {ID: "right", Version: "1.0.0", Extends: parentKey},
	}
	resolved, err := ResolveAllManifests(catalog)
	testutil.FailErr(t, "resolveAllManifests", err)
	if got := resolved[parentKey].Parameters["depth"].Default; got != "light" {
		t.Fatalf("parent depth = %q want light", got)
	}
	if got := resolved[ManifestKey("right", "1.0.0")].Parameters["depth"].Default; got != "light" {
		t.Fatalf("sibling depth = %q want light", got)
	}
	left := resolved[ManifestKey("left", "1.0.0")]
	left.Parameters["new"] = WorkflowParameter{Type: "depth", Default: "none"}
	if _, leaked := resolved[parentKey].Parameters["new"]; leaked {
		t.Fatal("mutating resolved child contaminated parent")
	}
}

func TestResolveManifestExtendsIsolatesInjects(t *testing.T) {
	phase := "build"
	parentKey := ManifestKey("base", "1.0.0")
	catalog := map[string]Manifest{
		parentKey: {
			ID: "base", Version: "1.0.0",
			Request: &ManifestRequest{Cadence: RequestCadenceOnce, Question: "What should this workflow do?"},
			Injects: []anchor.WorkflowInject{{
				Selector: anchor.Selector{
					Phase:    &phase,
					Tools:    []string{"read"},
					Surfaces: []string{"coordinator"},
				},
				Dedup: yaml.Node{Content: []*yaml.Node{{Value: "turn"}}},
			}},
		},
		ManifestKey("child", "1.0.0"): {ID: "child", Version: "1.0.0", Extends: parentKey},
	}
	resolved, err := ResolveAllManifests(catalog)
	testutil.FailErr(t, "resolveAllManifests", err)
	child := resolved[ManifestKey("child", "1.0.0")]
	child.Injects[0].Selector.Tools[0] = "write"
	child.Injects[0].Selector.Surfaces[0] = "worker"
	child.Injects[0].Dedup.Content[0].Value = "session"

	parent := resolved[parentKey].Injects[0]
	if parent.Selector.Tools[0] != "read" {
		t.Fatalf("parent tool = %q want read", parent.Selector.Tools[0])
	}
	if got := parent.Selector.Surfaces[0]; got != "coordinator" {
		t.Fatalf("parent surface = %q want coordinator", got)
	}
	if got := parent.Dedup.Content[0].Value; got != "turn" {
		t.Fatalf("parent dedup = %q want turn", got)
	}
}

func TestResolveManifestExtendsReportFalseOverridesParent(t *testing.T) {
	parent := Manifest{ID: "base", Version: "1.0.0", Controls: ManifestControls{Report: &ReportControls{Enabled: true}}}
	child := Manifest{ID: "child", Version: "1.0.0", Controls: ManifestControls{Report: &ReportControls{Enabled: false}}}
	merged := mergeManifest(parent, child)
	if merged.ReportEnabled() {
		t.Fatal("child controls.report.enabled=false must disable inherited reports")
	}
}

func TestResolveManifestExtendsFalseFlagsOverrideParent(t *testing.T) {
	enabled := true
	disabled := false
	merged := mergeManifest(
		Manifest{ID: "base", Version: "1.0.0", Featured: &enabled, RequiresRepo: &enabled},
		Manifest{ID: "child", Version: "1.0.0", Featured: &disabled, RequiresRepo: &disabled},
	)
	if manifestBool(merged.Featured) || manifestBool(merged.RequiresRepo) {
		t.Fatalf("merged flags featured=%v requires_repo=%v, want false", manifestBool(merged.Featured), manifestBool(merged.RequiresRepo))
	}
}

func TestRegistryCopiesManifestState(t *testing.T) {
	manifest := Manifest{
		ID: "copied", Version: "1.0.0",
		Parameters: map[string]WorkflowParameter{"depth": {Type: "depth", Default: "light"}},
	}
	registry := NewRegistry(map[string]Manifest{ManifestKey(manifest.ID, manifest.Version): manifest})
	manifest.Parameters["depth"] = WorkflowParameter{Type: "depth", Default: "none"}
	loaded, err := registry.Get("copied", "1.0.0")
	testutil.FailErr(t, "Get", err)
	loaded.Parameters["depth"] = WorkflowParameter{Type: "depth", Default: "thorough"}
	reloaded, err := registry.Get("copied", "1.0.0")
	testutil.FailErr(t, "Get again", err)
	if got := reloaded.Parameters["depth"].Default; got != "light" {
		t.Fatalf("registry depth = %q want light", got)
	}
}
