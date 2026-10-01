package contract

import (
	"path/filepath"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledPlanManifestDiscoveryFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "plan", "workflows", "plan", "workflow.yaml")
	m, err := workflowdef.LoadManifestFromFile(path)
	contractcheck.FailErr(t, "workflow.LoadManifestFromFile failed", err)
	if m.ID != "plan" || m.Version != "1.0.0" {
		t.Fatalf("id/version = %s@%s", m.ID, m.Version)
	}
	if m.Trigger != "/plan" {
		t.Fatalf("trigger = %q", m.Trigger)
	}
	wantPhases := []string{"research", "expand", "approve", "execute", "done"}
	if len(m.Phases) != len(wantPhases) {
		t.Fatalf("phases = %v", m.Phases)
	}
	for i, p := range wantPhases {
		if m.Phases[i] != p {
			t.Fatalf("phase[%d] = %q want %q", i, m.Phases[i], p)
		}
	}
}
