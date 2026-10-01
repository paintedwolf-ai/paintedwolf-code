package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// writeProjectOverlay creates an untrusted on-disk overlay.
func writeProjectOverlay(t *testing.T, projectDir, body string) {
	t.Helper()
	dir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create overlay dir", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "approvals.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write overlay", err)
	}
}

// Project overlays can only tighten the device posture.
func TestProjectOverlayCannotLowerPosture(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)

	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}); err != nil {
		testutil.FailErr(t, "store.PutGlobal strict failed", err)
	}
	writeProjectOverlay(t, projectDir, "approval_posture: light\n")

	got := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir}).Posture
	if got != gate.PostureStrict {
		t.Fatalf("posture with hostile overlay = %q, want strict (project may not loosen)", got)
	}
}

func TestProjectOverlayMayRaisePosture(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)

	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureBalanced}); err != nil {
		testutil.FailErr(t, "store.PutGlobal balanced failed", err)
	}
	writeProjectOverlay(t, projectDir, "approval_posture: strict\n")

	got := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir}).Posture
	if got != gate.PostureStrict {
		t.Fatalf("posture with tightening overlay = %q, want strict", got)
	}
}

// Invalid authority-shaped policy rows in a checked-in project file are ignored.
func TestProjectOverlayInvalidEffectsAreDropped(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)

	writeProjectOverlay(t, projectDir, `
rules:
  - category: tool
    pattern: "*"
    effect: permit
  - category: command
    pattern: "rm *"
    effect: deny
`)

	rules := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir}).Rules
	var sawDeny bool
	for _, r := range rules {
		if r.Effect == settings.ApprovalEffect("permit") {
			t.Fatalf("project overlay contributed an invalid rule: %+v", r)
		}
		if r.Category == settings.ApprovalCategoryCommand && r.Pattern == "rm *" {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Fatal("project overlay deny rule was dropped; the layer must still be able to tighten")
	}
}

func TestPutProjectRejectsInvalidEffects(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)

	err = store.PutProject(projectDir, settings.ApprovalConfig{
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryTool, Pattern: "*", Effect: settings.ApprovalEffect("permit")},
			{Category: settings.ApprovalCategoryTool, Pattern: "delete", Effect: settings.ApprovalEffectAsk},
		},
	})
	if err == nil {
		t.Fatal("PutProject accepted an invalid policy effect")
	}
}
