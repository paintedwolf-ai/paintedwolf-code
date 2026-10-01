package rules_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectRulesOverlayMerge(t *testing.T) {
	engine := newBundledPostureEngine(t)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()
	rulesDir := rules.ProjectRulesDir(dir)
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "custom.yaml"), []byte(`
rules:
  - when: tool_is_write
    code: PROJECT_DENY_WRITE
    message: project blocks write
`), 0o644); err != nil {
		testutil.FailErr(t, "write custom rule", err)
	}
	if err := overlay.WarmOverlays([]string{dir}); err != nil {
		testutil.FailErr(t, "warm overlay", err)
	}
	engine.Overlay = overlay
	engine.ProjectSettingsApply = func(context.Context, string) bool { return true }

	ctx := context.Background()
	allow, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "write", ProjectDir: dir, ProjectID: "p1"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if allow.Allowed {
		t.Fatal("expected project overlay deny on write")
	}
	if allow.Code != "PROJECT_DENY_WRITE" {
		t.Fatalf("code = %q", allow.Code)
	}

	stillAllow, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "delegate_dispatch", ProjectDir: dir, ProjectID: "p1"}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !stillAllow.Allowed {
		t.Fatalf("expected bundled allow for delegation, got %q", stillAllow.Code)
	}
}

func TestRulesActiveRootWinsOnTie(t *testing.T) {
	engine := newBundledPostureEngine(t)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	overlay := rules.NewProjectRulesOverlay(reg)
	primary := t.TempDir()
	active := t.TempDir()
	writeProjectRule(t, primary, "primary blocks")
	writeProjectRule(t, active, "active blocks")

	paths := []string{primary, active}
	if err := overlay.WarmOverlays(paths); err != nil {
		testutil.FailErr(t, "overlay.WarmOverlays failed", err)
	}
	engine.Overlay = overlay
	engine.ProjectSettingsApply = func(context.Context, string) bool { return true }

	ctx := context.Background()
	outcome, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureBuild, ToolName: "write", ProjectID: "p1"}, OverlayRootPaths: paths})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if outcome.Allowed {
		t.Fatal("expected active root deny on write")
	}
	if outcome.Message != "active blocks" {
		t.Fatalf("message = %q want active root rule", outcome.Message)
	}
}

func TestProjectRulesInvalidYAMLFailsLoad(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()
	rulesDir := rules.ProjectRulesDir(dir)
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "bad.yaml"), []byte(`rules: [{`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := overlay.WarmOverlays([]string{dir}); err == nil {
		t.Fatal("expected invalid yaml error")
	}
}

func TestProjectRulesUnknownFieldFailsLoad(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build condition registry", err)
	testutil.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()
	rulesDir := rules.ProjectRulesDir(dir)
	testutil.FailErr(t, "create rules directory", os.MkdirAll(rulesDir, 0o755))
	testutil.FailErr(t, "write rules", os.WriteFile(filepath.Join(rulesDir, "rules.yaml"), []byte(`
rules:
  - when: tool_is_write
    cod: TYPO
`), 0o644))
	if err := overlay.WarmOverlays([]string{dir}); err == nil {
		t.Fatal("expected unknown rule field to fail closed")
	}
}

func TestProjectRulesEmptyEntryFailsLoad(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build condition registry", err)
	testutil.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()
	rulesDir := rules.ProjectRulesDir(dir)
	testutil.FailErr(t, "create rules directory", os.MkdirAll(rulesDir, 0o755))
	testutil.FailErr(t, "write rules", os.WriteFile(filepath.Join(rulesDir, "rules.yaml"), []byte("rules: [{}]\n"), 0o644))
	if err := overlay.WarmOverlays([]string{dir}); err == nil {
		t.Fatal("expected empty rule entry to fail closed")
	}
}

func TestProjectRulesWarmRefusesOverlayFormatTooNew(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()
	lycaonDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(lycaonDir, 0o700); err != nil {
		testutil.FailErr(t, "mkdir overlay", err)
	}
	if err := os.WriteFile(filepath.Join(lycaonDir, "overlay.yaml"), []byte(fmt.Sprintf("overlay_format: %d\n", settingsoverlay.MaxFormat+1)), 0o600); err != nil {
		testutil.FailErr(t, "write overlay.yaml", err)
	}
	err = overlay.WarmOverlays([]string{dir})
	if err == nil {
		t.Fatal("expected overlay_format too new")
	}
	if !errors.Is(err, settingsoverlay.ErrFormatTooNew) {
		t.Fatalf("err = %v want ErrFormatTooNew", err)
	}
}

func TestProjectRulesWarmRefusesSecondaryOverlayFormat(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build condition registry", err)
	testutil.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	overlay := rules.NewProjectRulesOverlay(reg)
	primary := t.TempDir()
	secondary := t.TempDir()
	overlayDir := filepath.Join(secondary, settingsoverlay.DirName())
	testutil.FailErr(t, "create secondary overlay", os.MkdirAll(overlayDir, 0o700))
	testutil.FailErr(t, "write secondary overlay format", os.WriteFile(
		filepath.Join(overlayDir, "overlay.yaml"), []byte(fmt.Sprintf("overlay_format: %d\n", settingsoverlay.MaxFormat+1)), 0o600,
	))

	err = overlay.WarmOverlays([]string{primary, secondary})
	if !errors.Is(err, settingsoverlay.ErrFormatTooNew) {
		t.Fatalf("WarmOverlays error = %v, want ErrFormatTooNew", err)
	}
}

func TestProjectRulesGetRefusesOverlayFormatThatBecameTooNew(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	testutil.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(reg))
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()
	writeProjectRule(t, dir, "first")
	testutil.FailErr(t, "warm", overlay.WarmOverlays([]string{dir}))

	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	testutil.FailErr(t, "write newer overlay format", os.WriteFile(
		filepath.Join(overlayDir, "overlay.yaml"), []byte(fmt.Sprintf("overlay_format: %d\n", settingsoverlay.MaxFormat+1)), 0o600,
	))
	_, err = overlay.GetOverlays([]string{dir})
	if !errors.Is(err, settingsoverlay.ErrFormatTooNew) {
		t.Fatalf("Get error = %v, want ErrFormatTooNew", err)
	}
}

func TestProjectRulesOverlayReloadsContentWithUnchangedMetadata(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		testutil.FailErr(t, "register rule conditions", err)
	}
	overlay := rules.NewProjectRulesOverlay(reg)
	dir := t.TempDir()

	writeProjectRule(t, dir, "first")
	testutil.FailErr(t, "warm", overlay.WarmOverlays([]string{dir}))
	got, err := overlay.GetOverlays([]string{dir})
	testutil.FailErr(t, "get warmed", err)
	if len(got.Rules) != 1 || got.Rules[0].Message != "first" {
		t.Fatalf("warmed rules = %#v", got.Rules)
	}

	rulePath := filepath.Join(rules.ProjectRulesDir(dir), "rules.yaml")
	info, err := os.Stat(rulePath)
	testutil.FailErr(t, "stat warmed rules", err)
	writeProjectRule(t, dir, "second")
	testutil.FailErr(t, "restore rules timestamp", os.Chtimes(rulePath, info.ModTime(), info.ModTime()))
	got, err = overlay.GetOverlays([]string{dir})
	testutil.FailErr(t, "get after out-of-band change", err)
	if len(got.Rules) != 1 || got.Rules[0].Message != "second" {
		t.Fatalf("rules = %#v, want the bytes now on disk", got.Rules)
	}

	testutil.FailErr(t, "remove rules file", os.Remove(filepath.Join(rules.ProjectRulesDir(dir), "rules.yaml")))
	got, err = overlay.GetOverlays([]string{dir})
	testutil.FailErr(t, "get after removal", err)
	if len(got.Rules) != 0 {
		t.Fatalf("rules = %#v, want none after the file was removed", got.Rules)
	}
}

func writeProjectRule(t *testing.T, rootDir, message string) {
	t.Helper()
	rulesDir := rules.ProjectRulesDir(rootDir)
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	body := "rules:\n  - when: tool_is_write\n    code: PROJECT_DENY_WRITE\n    message: " + message + "\n"
	if err := os.WriteFile(filepath.Join(rulesDir, "rules.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}
