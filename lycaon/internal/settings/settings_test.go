package settings_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

// testSettingsPaths returns the host-side (user/global) file paths a store
// writes through, plus a project dir. Bundled defaults come from the shipped
// catalog, the base of every merge asserted below.
func testSettingsPaths(t *testing.T) (globalPerms, globalLimits, projectDir string) {
	t.Helper()
	tmp := t.TempDir()
	projectDir = filepath.Join(tmp, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	return filepath.Join(tmp, "approvals.yaml"), filepath.Join(tmp, "limits.yaml"), projectDir
}

func TestApprovalStorePostureAndMerge(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)

	// Default posture is Balanced.
	if store.Posture() != gate.PostureBalanced {
		t.Fatalf("default posture = %q, want balanced", store.Posture())
	}

	// The bundle ships no rules — every default ask comes from the classifier + posture.
	effective := store.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
	if len(effective.Rules) != 0 {
		t.Fatalf("bundled defaults must carry no rules, got %+v", effective.Rules)
	}

	// Posture is authoritative + persisted; Strict derives egress "ask", Light derives "observe".
	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}); err != nil {
		testutil.FailErr(t, "store.PutGlobal strict failed", err)
	}
	if store.Posture() != gate.PostureStrict {
		t.Fatalf("posture after PutGlobal = %q, want strict", store.Posture())
	}
	if store.EgressPosture() != confine.PostureAsk {
		t.Fatalf("strict egress = %v, want ask", store.EgressPosture())
	}
	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight}); err != nil {
		testutil.FailErr(t, "store.PutGlobal light failed", err)
	}
	if store.EgressPosture() != confine.PostureObserve {
		t.Fatalf("light egress = %v, want observe", store.EgressPosture())
	}

	// Policy rules persist and layer under the posture.
	if err := store.PutGlobal(settings.ApprovalConfig{
		Posture: gate.PostureBalanced,
		Rules: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryTool,
			Pattern:  "custom_tool",
			Effect:   settings.ApprovalEffectAsk,
		}},
	}); err != nil {
		testutil.FailErr(t, "store.PutGlobal policy failed", err)
	}
	withCustom := store.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
	foundCustom := false
	for _, r := range withCustom.Rules {
		if r.Pattern == "custom_tool" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatal("expected preserved custom grant")
	}

	if err := store.PutProject(projectDir, settings.ApprovalConfig{Rules: []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool,
		Pattern:  "write",
		Effect:   settings.ApprovalEffectDeny,
	}}}); err != nil {
		t.Fatal(err)
	}
	projectEffective := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir})
	for _, r := range projectEffective.Rules {
		if r.Pattern == "write" && r.Effect != settings.ApprovalEffectDeny {
			t.Fatalf("project deny should override bundled ask for write: %+v", r)
		}
	}
}

func TestApprovalFileMode0600(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	if err := store.PutGlobal(settings.ApprovalConfig{Rules: []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool,
		Pattern:  "command",
		Effect:   settings.ApprovalEffectAsk,
	}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(global)
	testutil.FailErr(t, "stat path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("approvals file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestApprovalStorePosturePersists(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight}); err != nil {
		testutil.FailErr(t, "store.PutGlobal failed", err)
	}
	// A fresh store reading the same file sees the persisted posture.
	reopened, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt reopen failed", err)
	if reopened.Posture() != gate.PostureLight {
		t.Fatalf("persisted posture = %q, want light", reopened.Posture())
	}
}

func TestApprovalStoreProjectPostureOverride(t *testing.T) {
	global, _, projectDir := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureBalanced}); err != nil {
		testutil.FailErr(t, "store.PutGlobal failed", err)
	}
	globalCfg := store.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
	if globalCfg.Posture != gate.PostureBalanced {
		t.Fatalf("global posture = %q, want balanced", globalCfg.Posture)
	}
	// Empty project overlay inherits global posture.
	inherited := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir})
	if inherited.Posture != gate.PostureBalanced {
		t.Fatalf("inherited project posture = %q, want balanced", inherited.Posture)
	}
	aiOff := false
	if err := store.PutProject(projectDir, settings.ApprovalConfig{
		Posture:     gate.PostureStrict,
		AIRationale: &aiOff,
	}); err != nil {
		testutil.FailErr(t, "store.PutProject failed", err)
	}
	projectCfg := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projectDir})
	if projectCfg.Posture != gate.PostureStrict {
		t.Fatalf("project posture = %q, want strict", projectCfg.Posture)
	}
	if projectCfg.AIRationale == nil || *projectCfg.AIRationale {
		t.Fatalf("project ai_rationale = %v, want false", projectCfg.AIRationale)
	}
	// Global unchanged.
	if store.Get(llm.SettingsScopeGlobal, settings.ProjectRef{}).Posture != gate.PostureBalanced {
		t.Fatal("global posture should remain balanced")
	}
	layers := store.MergedFrom(llm.SettingsScopeProject, projectDir)
	foundProject := false
	for _, layer := range layers {
		if layer == "project" {
			foundProject = true
		}
	}
	if !foundProject {
		t.Fatalf("MergedFrom = %v, want project layer", layers)
	}
}

func TestLimitsStoreMerge(t *testing.T) {
	_, globalLimits, projectDir := testSettingsPaths(t)
	store, err := settings.NewLimitsStoreAt(globalLimits)
	testutil.FailErr(t, "settings.NewLimitsStoreAt failed", err)
	base := store.Get(llm.SettingsScopeGlobal, "")
	if base.MaxIterations != 500 {
		t.Fatalf("bundled max_iterations = %d", base.MaxIterations)
	}
	if err := store.PutGlobal(settings.SessionLimits{MaxToolResultBytes: 12345}); err != nil {
		testutil.FailErr(t, "store.PutGlobal failed", err)
	}
	global := store.Get(llm.SettingsScopeGlobal, "")
	if global.MaxToolResultBytes != 12345 {
		t.Fatalf("global max_tool_result_bytes = %v", global.MaxToolResultBytes)
	}
	if err := store.PutProject(projectDir, settings.SessionLimits{MaxIterations: 3}); err != nil {
		testutil.FailErr(t, "store.PutProject failed", err)
	}
	project := store.Get(llm.SettingsScopeProject, projectDir)
	if project.MaxIterations != 3 {
		t.Fatalf("project max_iterations = %d", project.MaxIterations)
	}
}

func TestLimitsStoreClearsEmptyProjectOverlay(t *testing.T) {
	_, globalLimits, projectDir := testSettingsPaths(t)
	store, err := settings.NewLimitsStoreAt(globalLimits)
	testutil.FailErr(t, "settings.NewLimitsStoreAt", err)
	testutil.FailErr(t, "store.PutProject", store.PutProject(projectDir, settings.SessionLimits{MaxIterations: 3}))
	testutil.FailErr(t, "store.PutProject clear", store.PutProject(projectDir, settings.SessionLimits{}))

	if got := store.Get(llm.SettingsScopeProject, projectDir).MaxIterations; got != 500 {
		t.Fatalf("max_iterations after clear = %d want 500", got)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".paintedwolf", "limits.yaml")); !os.IsNotExist(err) {
		t.Fatalf("project limits file after clear: %v", err)
	}
}

func TestLimitsStoreProjectResetIsIdempotent(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			_, globalLimits, projectDir := testSettingsPaths(t)
			store, err := settings.NewLimitsStoreAt(globalLimits)
			testutil.FailErr(t, "open limits store", err)
			testutil.FailErr(t, "set device ceiling", store.PutGlobal(settings.SessionLimits{
				SpendCeilingEnabled: true, SessionSpendCeilingUSD: 11,
			}))
			if existing {
				testutil.FailErr(t, "set project ceiling", store.PutProject(projectDir, settings.SessionLimits{
					SpendCeilingEnabled: true, SessionSpendCeilingUSD: 3,
				}))
			}
			for range 2 {
				testutil.FailErr(t, "reset project ceiling", store.PutProject(projectDir, settings.SessionLimits{}))
				if got := store.Get(llm.SettingsScopeProject, projectDir).SessionSpendCeilingUSD; got != 11 {
					t.Fatalf("effective ceiling = %v, want device ceiling 11", got)
				}
				for _, layer := range store.MergedFrom(llm.SettingsScopeProject, projectDir) {
					if layer == "project" {
						t.Fatal("reset retained a project overlay")
					}
				}
			}
		})
	}
}
