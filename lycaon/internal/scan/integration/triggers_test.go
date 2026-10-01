package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

func sastRegistry(scanners ...string) *scan.MockRegistry {
	items := make([]scan.CodeScanner, 0, len(scanners))
	for _, id := range scanners {
		items = append(items, &scan.MockScanner{IDVal: id, CategoryList: []api.ScanCategory{api.ScanCategorySAST}})
	}
	return &scan.MockRegistry{Scanners: items}
}

func promotionTask(dir, delegationID string) api.WorkerTask {
	return api.WorkerTask{
		ID:            "job-1",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: dir,
		DelegationID:  delegationID,
		WorkflowRunID: "run-1",
	}
}

func TestPrepareOverlayPromotionAttributesExactSASTAndPaths(t *testing.T) {
	dir := testProjectDir(t)
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	testutil.FailErr(t, "write main.go", os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644))

	triggers := &scan.TriggerService{Registry: sastRegistry("sast-one"), Gates: scancfg.DefaultGatesConfig()}
	plan, err := triggers.PrepareOverlayPromotion(context.Background(), promotionTask(dir, "dep-1"), []string{
		"src/main.go", filepath.Join(dir, "src", "main.go"), "../outside.go", " ",
	}, nil)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)

	if !plan.Required || plan.ScannerID != "sast-one" || plan.ScanID == "" {
		t.Fatalf("plan obligation = %#v", plan)
	}
	if len(plan.ChangedPaths) != 1 || plan.ChangedPaths[0] != "src/main.go" {
		t.Fatalf("changed paths = %#v", plan.ChangedPaths)
	}
	if plan.DelegationID != "dep-1" || plan.WorkflowRunID != "run-1" || plan.WorkerJobID != "job-1" {
		t.Fatalf("attribution = %#v", plan)
	}
}

func TestPrepareOverlayPromotionDeletionScansSurvivingParent(t *testing.T) {
	dir := testProjectDir(t)
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))

	triggers := &scan.TriggerService{Registry: sastRegistry("sast-one"), Gates: scancfg.DefaultGatesConfig()}
	plan, err := triggers.PrepareOverlayPromotion(context.Background(), promotionTask(dir, "dep-delete"), []string{"src/removed.go"}, []string{"src/removed.go"})
	testutil.FailErr(t, "PrepareOverlayPromotion", err)

	if len(plan.DeletedPaths) != 1 || plan.DeletedPaths[0] != "src/removed.go" {
		t.Fatalf("deleted paths = %#v", plan.DeletedPaths)
	}
}

func TestPrepareOverlayPromotionDoesNotAbsorbAmbientGitDirt(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "write ambient dirt", os.WriteFile(filepath.Join(dir, "ambient.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "write landed file", os.WriteFile(filepath.Join(dir, "landed.go"), []byte("package main\n"), 0o644))

	triggers := &scan.TriggerService{Registry: sastRegistry("sast-one"), Gates: scancfg.DefaultGatesConfig()}
	plan, err := triggers.PrepareOverlayPromotion(context.Background(), promotionTask(dir, "dep-git"), []string{"landed.go"}, nil)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	if len(plan.ChangedPaths) != 1 || plan.ChangedPaths[0] != "landed.go" {
		t.Fatalf("changed paths absorbed unrelated workspace dirt: %#v", plan.ChangedPaths)
	}
}

func TestPrepareOverlayPromotionFullRootKeepsExactAttribution(t *testing.T) {
	dir := testProjectDir(t)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	gates := scancfg.DefaultGatesConfig()
	gates.Gates.LandedChange.Scope = scancfg.LandedChangeScopeFullRoot

	plan, err := (&scan.TriggerService{Registry: sastRegistry("sast-one"), Gates: gates}).PrepareOverlayPromotion(
		context.Background(), promotionTask(dir, "dep-full"), []string{"main.go"}, nil,
	)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	if !plan.Required || plan.PathScoped || len(plan.ChangedPaths) != 1 {
		t.Fatalf("full-root plan = %#v", plan)
	}
}

func TestPrepareOverlayPromotionRecordsNoObligationWhenDisabled(t *testing.T) {
	dir := testProjectDir(t)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	gates := scancfg.DefaultGatesConfig()
	gates.Gates.LandedChange.Enabled = false

	plan, err := (&scan.TriggerService{Registry: sastRegistry("sast-one"), Gates: gates}).PrepareOverlayPromotion(
		context.Background(), promotionTask(dir, "dep-disabled"), []string{"main.go"}, nil,
	)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	if plan.Required || len(plan.ChangedPaths) != 1 || plan.ID == "" {
		t.Fatalf("disabled landing plan = %#v", plan)
	}
}

func TestPrepareOverlayPromotionHonorsMainSwitch(t *testing.T) {
	dir := testProjectDir(t)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	secStore, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "security.yaml"))
	testutil.FailErr(t, "NewSecurityScannersStoreAt", err)
	testutil.FailErr(t, "PutGlobal", secStore.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: boolPtr(false)}))

	plan, err := (&scan.TriggerService{
		Registry: sastRegistry("sast-one"), Gates: scancfg.DefaultGatesConfig(), Settings: secStore,
	}).PrepareOverlayPromotion(context.Background(), promotionTask(dir, "dep-off"), []string{"main.go"}, nil)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	if plan.Required || len(plan.ChangedPaths) != 1 {
		t.Fatalf("main-off landing plan = %#v", plan)
	}
}

func TestPrepareOverlayPromotionFailsClosedOnScannerAmbiguity(t *testing.T) {
	dir := testProjectDir(t)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	plan, err := (&scan.TriggerService{
		Registry: sastRegistry("sast-a", "sast-b"), Gates: scancfg.DefaultGatesConfig(),
	}).PrepareOverlayPromotion(context.Background(), promotionTask(dir, "dep-ambiguous"), []string{"main.go"}, nil)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	if !plan.Required || plan.ScannerID != "" || plan.InitialFailure == "" {
		t.Fatalf("ambiguous scanner plan = %#v", plan)
	}
}

func boolPtr(v bool) *bool { return &v }
