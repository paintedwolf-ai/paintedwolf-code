package blueprint

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestPlanToolPersistsEvidenceForTheSessionProject(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(NewFileStoreForTest(dir))
	mgr.DataDir = t.TempDir()
	bp, err := mgr.Create(t.Context(), "project", "review", "", "plan", "")
	testutil.FailErr(t, "create plan", err)
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register plan tool", RegisterPlanTools(registry, mgr))
	invocation := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project"},
		Source:   tools.InvocationSource{ActiveRootID: "root", Roots: []projectroot.RootRef{{ID: "root", Path: dir}}},
	}
	raw, err := registry.Run(t.Context(), "plan_append_review_evidence", map[string]any{"blueprint_path": bp.Path, "evidence": "reviewed declaration scope"}, invocation)
	testutil.FailErr(t, "append plan evidence", err)
	if !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("evidence acknowledgement = %s", raw)
	}
	path, err := mgr.criticEvidencePath("project", bp.Path)
	testutil.FailErr(t, "resolve evidence file", err)
	content, err := os.ReadFile(path)
	testutil.FailErr(t, "read persisted evidence", err)
	if string(content) != "reviewed declaration scope\n" {
		t.Fatalf("persisted evidence = %q", content)
	}
	if _, err := registry.Run(t.Context(), "plan_append_review_evidence", map[string]any{"blueprint_path": ConventionPath("missing.md"), "evidence": "rejected"}, invocation); err == nil {
		t.Fatal("missing blueprint accepted evidence")
	}
	content, err = os.ReadFile(path)
	testutil.FailErr(t, "read evidence after refusal", err)
	if string(content) != "reviewed declaration scope\n" {
		t.Fatalf("refused tool changed evidence = %q", content)
	}
}
