package security

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/scan"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanCompareVerifyFixE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("scan_compare verify-fix E2E skipped in -short")
	}

	src := scanFixtureDir(t)
	projectDir := copyDirToTemp(t, src)
	gittest.InitCommit(t, projectDir, "fixture baseline")

	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	baseline := waitScanPackComplete(t, h, projectDir, []any{string(wire.ScanCategorySecret)}, 90*time.Second)
	if baseline.Status != wire.CodeScanStatusComplete {
		t.Fatalf("baseline status = %q", baseline.Status)
	}
	oldID := secretEngineScanID(t, baseline)
	if oldID == "" {
		t.Fatalf("no secret scan in baseline: %+v", baseline.PerScan)
	}

	secretsPath := filepath.Join(projectDir, "secrets.env")
	if err := os.WriteFile(secretsPath, []byte("# remediated\n"), 0o644); err != nil {
		testutil.FailErr(t, "write fixed secrets.env", err)
	}
	gittest.CommitAll(t, projectDir, "fix secrets")
	// Publish fixture writes before requesting the next snapshot.
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: projectDir, Kind: repochange.WorktreeChanged,
		Paths: []string{secretsPath}, Source: repochange.SourceMutation,
	})

	after := waitScanPackComplete(t, h, projectDir, []any{string(wire.ScanCategorySecret)}, 90*time.Second)
	if after.Status != wire.CodeScanStatusComplete {
		t.Fatalf("after status = %q", after.Status)
	}
	newID := secretEngineScanID(t, after)
	if newID == "" || newID == oldID {
		t.Fatalf("new secret scan id = %q old = %q", newID, oldID)
	}

	project := createProjectHTTP(t, h.Server, projectDir)
	for _, id := range []string{oldID, newID} {
		rec := getCodeScan(t, h.Server, project.ID, id)
		if rec.CoverageStatus != wire.ScanCoverageComplete || rec.FindingSetID == "" {
			t.Fatalf("scan %s lacks a complete baseline: coverage=%s capture=%s admission=%s target=%s finding_set=%s warnings=%+v", id, rec.CoverageStatus, rec.SourceCaptureQuality, rec.SourceAdmissionMode, rec.TargetKind, rec.FindingSetID, rec.Warnings)
		}
	}
	raw, err := h.ToolRegistry.Run(context.Background(), "scan_compare", map[string]any{
		"old_scan_id": oldID,
		"new_scan_id": newID,
	}, securityToolContext("", projectDir, "implement"))
	testutil.FailErr(t, "scan_compare tool", err)

	var resp scan.Comparison
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		testutil.FailErr(t, "unmarshal compare", err)
	}
	if resp.NewCount != 0 {
		t.Fatalf("new_count = %d want 0 after fix; resp=%+v", resp.NewCount, resp)
	}
	if resp.ResolvedCount < 1 {
		t.Fatalf("resolved_count = %d want >= 1; resp=%+v", resp.ResolvedCount, resp)
	}
	if len(resp.ResolvedFindings) == 0 {
		t.Fatalf("resolved_findings empty: %+v", resp)
	}
}

func secretEngineScanID(t *testing.T, payload scantoolapi.ScanPackToolResult) string {
	t.Helper()
	for _, row := range payload.PerScan {
		if row.ScannerID == "lycaon-secrets" && row.FindingsCount > 0 {
			return row.ScanID
		}
	}
	for _, row := range payload.PerScan {
		if row.ScannerID == "lycaon-secrets" {
			return row.ScanID
		}
	}
	return ""
}

func copyDirToTemp(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	testutil.FailErr(t, "copy fixture", copyDir(src, dst))
	return dst
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, ".git") || strings.Contains(rel, "/"+settingsoverlay.DirName()+"/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
