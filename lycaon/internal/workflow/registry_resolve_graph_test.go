package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func graphManifest(t *testing.T, source string) workflowdef.Manifest {
	t.Helper()
	m, err := workflowdef.ParseManifestYAML([]byte(source))
	testutil.FailErr(t, "parse graph manifest", err)
	return m
}

const graphParentManifest = `id: parent
version: 1.0.0
name: Parent
request:
  question: What should happen?
phases:
  - id: done
    activity_label: Done
    terminal: true
`
const graphChildManifest = `id: child
version: 1.0.0
extends: parent@1.0.0
`

func TestResolveProjectInheritanceIndependentOfDirectoryOrder(t *testing.T) {
	for _, parentDir := range []string{"a-parent", "z-parent"} {
		t.Run(parentDir, func(t *testing.T) {
			root := t.TempDir()
			for dir, source := range map[string]string{parentDir: graphParentManifest, "m-child": graphChildManifest} {
				path := filepath.Join(root, settingsoverlay.DirName(), "workflows", dir)
				testutil.FailErr(t, "create workflow directory", os.MkdirAll(path, 0o755))
				testutil.FailErr(t, "write workflow", os.WriteFile(filepath.Join(path, "workflow.yaml"), []byte(source), 0o644))
			}
			resolver := ManifestResolver{ProjectTierApplies: func(context.Context, string) bool { return true }}
			reg, _, excluded, err := resolver.ResolveWithExcluded(t.Context(), root, "")
			testutil.FailErr(t, "resolve project graph", err)
			if len(excluded) != 0 {
				t.Fatalf("valid graph excluded: %+v", excluded)
			}
			child, err := reg.Get("child", "1.0.0")
			testutil.FailErr(t, "read inherited child", err)
			if child.Name != "Parent" || child.Request == nil || len(child.Phases) != 1 {
				t.Fatalf("inherited child: %+v", child)
			}
		})
	}
}

func TestResolveGraphRecomputesBundledDependentsAfterOverride(t *testing.T) {
	c := manifestCandidates{}
	parent := graphManifest(t, graphParentManifest)
	child := graphManifest(t, graphChildManifest)
	c.add(manifestCandidate{manifest: parent, scope: string(api.WorkflowScopeBundled)})
	c.add(manifestCandidate{manifest: child, scope: string(api.WorkflowScopeBundled)})
	parent.Name = "Project parent"
	c.add(manifestCandidate{manifest: parent, scope: string(api.WorkflowScopeProject), path: "project-parent"})
	parent.Name = "Session parent"
	c.add(manifestCandidate{manifest: parent, scope: string(api.WorkflowScopeSession), path: "session-parent"})
	resolved, scopes, excluded := c.resolve()
	if len(excluded) != 0 {
		t.Fatalf("valid overrides excluded: %+v", excluded)
	}
	if got := resolved["child@1.0.0"]; got.Name != "Session parent" {
		t.Fatalf("inherited name: %q", got.Name)
	}
	if scopes["parent@1.0.0"] != string(api.WorkflowScopeSession) || scopes["child@1.0.0"] != string(api.WorkflowScopeBundled) {
		t.Fatalf("scopes: %+v", scopes)
	}
}

func TestResolveGraphRejectsInvalidOverrideBeforeResolvingDependents(t *testing.T) {
	for _, invalid := range []string{
		"id: parent\nversion: 1.0.0\nextends: missing@1.0.0\n",
		"id: parent\nversion: 1.0.0\nphases:\n  - id: done\n    activity_label: Done\n    terminal: true\n",
		"id: parent\nversion: 1.0.0\nextends: child@1.0.0\n",
	} {
		t.Run(invalid, func(t *testing.T) {
			c := manifestCandidates{}
			c.add(manifestCandidate{manifest: graphManifest(t, graphParentManifest), scope: string(api.WorkflowScopeBundled)})
			c.add(manifestCandidate{manifest: graphManifest(t, graphChildManifest), scope: string(api.WorkflowScopeBundled)})
			c.add(manifestCandidate{manifest: graphManifest(t, invalid), scope: string(api.WorkflowScopeProject), path: "invalid-parent"})
			resolved, scopes, excluded := c.resolve()
			if len(excluded) != 1 || excluded[0].Path != "invalid-parent" {
				t.Fatalf("excluded: %+v", excluded)
			}
			if len(resolved) != 2 || resolved["child@1.0.0"].Name != "Parent" {
				t.Fatalf("valid lower graph: %+v", resolved)
			}
			if scopes["parent@1.0.0"] != string(api.WorkflowScopeBundled) {
				t.Fatalf("parent scope: %q", scopes["parent@1.0.0"])
			}
		})
	}
}

func TestResolveGraphIsolatesCycleFromIndependentWorkflows(t *testing.T) {
	c := manifestCandidates{}
	for _, source := range []string{
		"id: a\nversion: 1.0.0\nextends: b@1.0.0\n",
		"id: b\nversion: 1.0.0\nextends: a@1.0.0\n",
		graphParentManifest,
	} {
		m := graphManifest(t, source)
		c.add(manifestCandidate{manifest: m, scope: string(api.WorkflowScopeSession), path: m.ID})
	}
	resolved, _, excluded := c.resolve()
	if len(excluded) != 2 || len(resolved) != 1 || resolved["parent@1.0.0"].Name != "Parent" {
		t.Fatalf("resolved=%+v excluded=%+v", resolved, excluded)
	}
}

func TestResolveGraphValidatesInvocationTargets(t *testing.T) {
	for _, target := range []string{"parent", "missing", "caller"} {
		t.Run(target, func(t *testing.T) {
			c := manifestCandidates{}
			parent := graphManifest(t, graphParentManifest)
			c.add(manifestCandidate{manifest: parent, scope: string(api.WorkflowScopeBundled)})
			caller := graphManifest(t, graphParentManifest)
			caller.ID = "caller"
			caller.Phases = []string{"invoke"}
			caller.PhaseDefs = []workflowdef.PhaseDef{{ID: "invoke", ActivityLabel: "Invoking workflow", InvokeWorkflow: &workflowdef.InvokeWorkflowSpec{
				WorkflowID: target, Version: "1.0.0", Blueprint: workflowdef.ChildBlueprintNone,
			}}}
			c.add(manifestCandidate{manifest: caller, scope: string(api.WorkflowScopeSession), path: "caller"})
			resolved, _, excluded := c.resolve()
			if target == "parent" {
				if len(resolved) != 2 || len(excluded) != 0 {
					t.Fatalf("valid invocation: resolved=%+v excluded=%+v", resolved, excluded)
				}
			} else if len(resolved) != 1 || len(excluded) != 1 || excluded[0].ID != "caller" {
				t.Fatalf("invalid invocation: resolved=%+v excluded=%+v", resolved, excluded)
			}
		})
	}
}

func TestResolveGraphProtectsBundledInvocationContract(t *testing.T) {
	c := manifestCandidates{}
	child := graphManifest(t, graphParentManifest)
	c.add(manifestCandidate{manifest: child, scope: string(api.WorkflowScopeBundled)})
	caller := graphManifest(t, graphParentManifest)
	caller.ID = "caller"
	caller.Phases = []string{"invoke"}
	caller.PhaseDefs = []workflowdef.PhaseDef{{ID: "invoke", ActivityLabel: "Invoking workflow", InvokeWorkflow: &workflowdef.InvokeWorkflowSpec{
		WorkflowID: child.ID, Version: child.Version, Blueprint: workflowdef.ChildBlueprintNone,
	}}}
	c.add(manifestCandidate{manifest: caller, scope: string(api.WorkflowScopeBundled)})
	child.Blueprint = &workflowdef.BlueprintDef{Path: "blueprint.md"}
	c.add(manifestCandidate{manifest: child, scope: string(api.WorkflowScopeProject), path: "project-child"})
	resolved, _, excluded := c.resolve()
	if len(excluded) != 1 || excluded[0].Path != "project-child" || len(resolved) != 2 || resolved["parent@1.0.0"].Blueprint != nil {
		t.Fatalf("bundled invocation contract: resolved=%+v excluded=%+v", resolved, excluded)
	}
}

func TestResolveGraphValidatesBundledDescendantAfterOverride(t *testing.T) {
	c := manifestCandidates{}
	parent := graphManifest(t, graphParentManifest)
	parent.PhaseDefs = append([]workflowdef.PhaseDef{{ID: "start", ActivityLabel: "Starting", Next: "done"}}, parent.PhaseDefs...)
	parent.Phases = []string{"start", "done"}
	c.add(manifestCandidate{manifest: parent, scope: string(api.WorkflowScopeBundled)})
	child := graphManifest(t, graphChildManifest)
	child.Phases = []string{"start"}
	child.PhaseDefs = []workflowdef.PhaseDef{{ID: "start", ActivityLabel: "Starting", Next: "done"}}
	c.add(manifestCandidate{manifest: child, scope: string(api.WorkflowScopeBundled)})
	parent.Phases = []string{"start", "replacement"}
	parent.PhaseDefs = []workflowdef.PhaseDef{
		{ID: "start", ActivityLabel: "Starting", Next: "replacement"},
		{ID: "replacement", ActivityLabel: "Done", Terminal: true},
	}
	c.add(manifestCandidate{manifest: parent, scope: string(api.WorkflowScopeProject), path: "project-parent"})
	resolved, scopes, excluded := c.resolve()
	if len(excluded) != 1 || excluded[0].Path != "project-parent" || len(resolved) != 2 || scopes["parent@1.0.0"] != string(api.WorkflowScopeBundled) {
		t.Fatalf("bundled descendant: resolved=%+v excluded=%+v scopes=%+v", resolved, excluded, scopes)
	}
	restored := resolved["child@1.0.0"]
	testutil.FailErr(t, "validate restored descendant", workflowdef.ValidatePhaseTargets(restored))
	start, ok := restored.PhaseByID("start")
	if !ok || start.Next != "done" {
		t.Fatalf("restored child start: %+v", start)
	}
	if _, ok := restored.PhaseByID("done"); !ok {
		t.Fatal("restored child missing inherited terminal phase")
	}
}
