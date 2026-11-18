package worker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromotionOrderReadsQualifiedSecondaryRoot(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	roots := []projectroot.RootRef{{ID: "primary", Label: "app", Path: primary, IsPrimary: true}, {ID: "secondary", Label: "library", Path: secondary}}
	testutil.FailErr(t, "write current primary", os.WriteFile(filepath.Join(secondary, "shared.txt"), []byte("current\n"), 0600))
	makeTask := func(id, before, after string) api.WorkerTask {
		branch := t.TempDir()
		var meta []workspace.JobMetaRoot
		for _, root := range roots {
			dir, err := projectroot.BranchDirForID(root.ID)
			testutil.FailErr(t, "resolve branch root", err)
			testutil.FailErr(t, "create branch root", os.MkdirAll(filepath.Join(branch, dir), 0700))
			meta = append(meta, workspace.JobMetaRoot{ID: root.ID, Label: root.Label, Path: root.Path, IsPrimary: root.IsPrimary})
		}
		testutil.FailErr(t, "record branch topology", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{Roots: meta, SnapshotComplete: true}))
		dir, err := projectroot.BranchDirForID("secondary")
		testutil.FailErr(t, "resolve edited root", err)
		file := filepath.Join(branch, dir, "shared.txt")
		testutil.FailErr(t, "write baseline", os.WriteFile(file, []byte(before), 0600))
		data := t.TempDir()
		baselines := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "baselines"))
		baseline, err := baselines.Capture(t.Context(), id, workspacebaseline.Branch(roots, branch))
		testutil.FailErr(t, "capture baseline", err)
		testutil.FailErr(t, "write branch change", os.WriteFile(file, []byte(after), 0600))
		return api.WorkerTask{ID: id, ParentSessionID: "parent", WorkspaceRootID: "primary", WorkspacePath: primary, WorkspaceRoot: branch, WorkspaceBaselinePath: baseline, Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"@library/shared.txt"}}}
	}
	task := makeTask("job", "base\n", "worker\n")
	sibling := makeTask("sibling", "current\n", "base\n")
	svc := &worker.MergeService{Sessions: overlapSessionLister{tasks: []api.WorkerTask{task, sibling}}}
	got, err := worker.AssessPromotePaths3Way(t.Context(), svc, "parent", &task, []string{"@library/shared.txt"})
	testutil.FailErr(t, "assess qualified path", err)
	if len(got.PathOrders) != 1 || got.PathOrders[0].Order != api.WorkerPromoteOrderCleanAfter || len(got.PathOrders[0].PromoteAfter) != 1 || got.PathOrders[0].PromoteAfter[0] != "sibling" {
		t.Fatalf("promotion order = %+v", got.PathOrders)
	}
}
