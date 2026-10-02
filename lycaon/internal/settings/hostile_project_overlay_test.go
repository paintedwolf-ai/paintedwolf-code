package settings_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func projectRefusals(store *settings.ApprovalStore, projectDir string) map[string]string {
	out := map[string]string{}
	for _, refused := range store.ProjectOverlay(projectDir).Rejected {
		out[refused.Entry] = refused.Code
	}
	return out
}

// A posture typo in repository policy applies the strictest posture a
// repository can ask for, keeps the rules beside it, and says why.
func TestProjectPostureTypoAppliesStrictAndIsReported(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	testutil.FailErr(t, "store.PutGlobal light", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight}))
	writeProjectOverlay(t, projectDir, `approval_posture: Ballanced
rules:
  - category: command
    pattern: "rm *"
    effect: deny
`)

	cfg := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir})
	if cfg.Posture != gate.PostureStrict {
		t.Fatalf("posture with a mistyped project posture = %q, want strict", cfg.Posture)
	}
	if got := effectFor(cfg.Rules, settings.ApprovalCategoryCommand, "rm *"); got != settings.ApprovalEffectDeny {
		t.Fatalf("a posture typo disabled the project's deny rule: effect=%q", got)
	}
	if got := projectRefusals(store, projectDir); got["approval_posture"] != settings.ProjectApprovalsInvalidEntry {
		t.Fatalf("posture typo was not reported: %v", got)
	}
}

// A repository approvals.yaml the host cannot read applies as strict with
// approvals on, rather than as no repository policy at all.
func TestUnreadableProjectApprovalsFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"syntax":      "rules: [\n",
		"unknown key": "approvals_posture: strict\n",
		"bad toggle":  "never_ask: maybe\n",
	} {
		t.Run(name, func(t *testing.T) {
			global, _, projectDir := testSettingsPaths(t)
			store, err := settings.NewApprovalStoreAt(global)
			testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
			on := true
			testutil.FailErr(t, "store.PutGlobal", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight, NeverAsk: &on}))
			writeProjectOverlay(t, projectDir, body)

			cfg := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir})
			if cfg.Posture != gate.PostureStrict || cfg.NeverAsk == nil || *cfg.NeverAsk {
				t.Fatalf("unreadable project approvals applied posture %q never_ask %v; want strict with approvals on", cfg.Posture, cfg.NeverAsk)
			}
			refused := store.ProjectOverlay(projectDir).Rejected
			if len(refused) != 1 || refused[0].Code != settings.ProjectApprovalsUnreadable || refused[0].Entry != "" {
				t.Fatalf("unreadable file reported as %+v", refused)
			}
		})
	}
}

// Parts that would grant or loosen, and malformed rules, are refused one by
// one and reported; valid rules beside them still apply.
func TestProjectApprovalRefusalsAreReported(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	writeProjectOverlay(t, projectDir, `rules:
  - category: tool
    pattern: "*"
    effect: allow
  - category: teleport
    pattern: "*"
    effect: ask
  - category: command
    pattern: "rm *"
    effect: deny
grants:
  - id: smuggled
never_ask: true
`)

	want := map[string]string{
		"rules[0]":  settings.ProjectApprovalsFieldsForbidden,
		"rules[1]":  settings.ProjectApprovalsInvalidEntry,
		"grants":    settings.ProjectApprovalsFieldsForbidden,
		"never_ask": settings.ProjectApprovalsFieldsForbidden,
	}
	got := projectRefusals(store, projectDir)
	for entry, code := range want {
		if got[entry] != code {
			t.Errorf("%s refused as %q, want %q (all: %v)", entry, got[entry], code, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("refusals = %v, want exactly %v", got, want)
	}
	rules := store.ProjectOverlay(projectDir).Config.Rules
	if len(rules) != 1 || rules[0].Pattern != "rm *" {
		t.Fatalf("applied project rules = %+v, want only the deny", rules)
	}
}

// Comments and blank documents are an empty overlay, not an unreadable one.
func TestEmptyProjectApprovalsAreNotRefused(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	writeProjectOverlay(t, projectDir, "# nothing yet\n")
	if refused := store.ProjectOverlay(projectDir).Rejected; len(refused) != 0 {
		t.Fatalf("empty overlay refused: %+v", refused)
	}
}

// A host write never replaces repository content it could not apply.
func TestProjectWritesRefuseToReplaceUnappliedContent(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	body := "approval_posture: stirct\nrules:\n  - category: command\n    pattern: \"rm *\"\n    effect: deny\n"
	writeProjectOverlay(t, projectDir, body)
	path := filepath.Join(projectDir, settingsoverlay.DirName(), "approvals.yaml")

	err = store.PutProject(projectDir, settings.ApprovalConfig{Posture: gate.PostureStrict})
	if !errors.Is(err, settings.ErrProjectApprovalsNeedRepair) {
		t.Fatalf("PutProject over an unapplied posture = %v, want ErrProjectApprovalsNeedRepair", err)
	}
	err = store.SetHostResourceRule(llm.SettingsScopeProject, projectDir, "camera", settings.ApprovalEffectAsk)
	if !errors.Is(err, settings.ErrProjectApprovalsNeedRepair) {
		t.Fatalf("SetHostResourceRule over an unapplied posture = %v, want ErrProjectApprovalsNeedRepair", err)
	}
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read overlay", err)
	if !bytes.Equal(data, []byte(body)) {
		t.Fatalf("refused write changed the file:\n%s", data)
	}
}

// Every decision reads the file as it is now, including after a host write.
func TestProjectApprovalsFollowEditsAfterAHostWrite(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	testutil.FailErr(t, "store.PutGlobal balanced", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureBalanced}))
	testutil.FailErr(t, "store.PutProject strict", store.PutProject(projectDir, settings.ApprovalConfig{Posture: gate.PostureStrict}))

	writeProjectOverlay(t, projectDir, "rules: []\n")
	if got := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir}).Posture; got != gate.PostureBalanced {
		t.Fatalf("posture after the file dropped its override = %q, want balanced", got)
	}
}

// A device posture typo stops startup with the file left as written.
func TestDevicePostureTypoRefusesToLoad(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	body := []byte("approval_posture: Ballanced\n")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(global), 0o755))
	testutil.FailErr(t, "write device approvals", os.WriteFile(global, body, 0o600))

	_, err := settings.NewApprovalStoreAt(global)
	if err == nil || !strings.Contains(err.Error(), "Ballanced") || !strings.Contains(err.Error(), global) {
		t.Fatalf("device posture typo loaded or was not named: %v", err)
	}
	data, readErr := os.ReadFile(global)
	testutil.FailErr(t, "read device approvals", readErr)
	if !bytes.Equal(data, body) {
		t.Fatalf("refused load changed the device file:\n%s", data)
	}
}
