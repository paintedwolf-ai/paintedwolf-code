package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssessPromoteSafeWithPrimaryContentBaselineDespiteOverlayMtime(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "f.go")
	branchRoot := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-overlay")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchRoot, 0o755))
	content := []byte("package f\n")
	testutil.FailErr(t, "WriteFile primary", os.WriteFile(primaryPath, content, 0o644))
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "Chtimes primary", os.Chtimes(primaryPath, past, past))

	branchPath := filepath.Join(branchRoot, "f.go")
	testutil.FailErr(t, "WriteFile branch", os.WriteFile(branchPath, append(content, '\n', '/'), 0o644))

	primaryBaseline := testbaseline.Capture(t, dir)

	raw := primaryBaseline
	task := &api.WorkerTask{
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branchRoot,
		WorkspaceBaselinePath: raw,
	}
	assessment, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess", task, []string{"f.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(assessment.CleanPaths) != 1 || assessment.CleanPaths[0] != "f.go" {
		t.Fatalf("expected safe merge with content baseline: %+v", assessment)
	}
}
