//go:build stress

package worker

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// largeSourceFiles is a working tree of the size a large real repository
// has; a mounted worker over it must copy and read nothing it is not asked for.
const largeSourceFiles = 100_000

// This explicitly exercises the mounted lane, not production budget selection.
// Restart, broad traversal and concurrent-worker scale are separate scenarios.
func TestStressMountedSparseWorkerLifecycleOverALargeSource(t *testing.T) {
	source := largeSource(t, largeSourceFiles)
	head := strings.TrimSpace(gittest.Run(t, source, "rev-parse", "HEAD"))
	original, err := os.ReadFile(filepath.Join(source, "README.md"))
	testutil.FailErr(t, "source read oracle", err)
	name := "scale-test-" + rand.Text() + ".txt"
	content := []byte("isolated worker change: " + name + "\n")
	sourceFile := filepath.Join(source, name)
	fixture := newLargeSourceWorkerFixture(t, source)
	started := time.Now()
	task := claimLargeSourceWorker(t, fixture, name)
	t.Logf("prepare elapsed=%s", time.Since(started))
	t.Cleanup(func() {
		testutil.FailErr(t, "destroy scale worker", fixture.manager.DestroyWorkerWorkspace(t.Context(), &workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
	})
	view, facts, _, ok := workspace.LookupBranchPreparedView(task.WorkspaceRoot)
	if !ok || view == nil || view.Core == nil || facts.PreparationKind != adapter.PreparationMounted {
		t.Fatal("scale fixture did not prepare an actual mounted worker")
	}
	t.Logf("selection=%+v preparation=%+v", view.Selection, view.Materialize)
	if view.Materialize.CopiedBytes != 0 || view.Materialize.CopiedFiles != 0 {
		t.Fatal("mounted preparation copied source working-tree content")
	}
	before := view.Core.Stats()
	started = time.Now()
	got, err := os.ReadFile(filepath.Join(task.WorkspaceRoot, "README.md"))
	testutil.FailErr(t, "read through worker mount", err)
	if !bytes.Equal(got, original) {
		t.Fatal("worker read differs from source oracle")
	}
	testutil.FailErr(t, "write through worker mount", os.WriteFile(filepath.Join(task.WorkspaceRoot, name), content, 0o644))
	if _, err := os.Lstat(sourceFile); !os.IsNotExist(err) {
		t.Fatalf("worker write reached source before promotion: %v", err)
	}
	t.Logf("sparse elapsed=%s counters before=%+v after=%+v", time.Since(started), before, view.Core.Stats())
	started = time.Now()
	gittest.Run(t, task.WorkspaceRoot, "add", "--", name)
	gittest.Run(t, task.WorkspaceRoot, "commit", "-m", "isolated scale change")
	t.Logf("private Git commit elapsed=%s", time.Since(started))
	started = time.Now()
	completed := fixture.completeWithChange(t, task, func() {})
	t.Logf("checkpoint elapsed=%s", time.Since(started))
	_, manifest, published, err := session.OpenTaskCheckpointStore(completed)
	testutil.FailErr(t, "scale checkpoint", err)
	if !published || len(manifest.Entries) != 1 || manifest.Entries[0].Path.Rel != name {
		t.Fatal("checkpoint scope differs from the single authored file")
	}
	service := &MergeService{Queue: fixture.queue, Store: fixture.queue, DataDir: testbaseline.DataDir(t, fixture.db), Workspace: fixture.manager}
	started = time.Now()
	result, err := service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	t.Logf("promotion elapsed=%s", time.Since(started))
	testutil.FailErr(t, "scale promotion", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("scale promotion = %+v", result)
	}
	assertFileHolds(t, sourceFile, string(content))
	assertFileHolds(t, filepath.Join(source, "README.md"), string(original))
	if got := gittest.Run(t, source, "rev-parse", "HEAD"); strings.TrimSpace(got) != head {
		t.Fatal("promotion changed source HEAD")
	}
}

func newLargeSourceWorkerFixture(t *testing.T, source string) *layeredWorkerFixture {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", source)
	branches := filepath.Join(testbaseline.DataDir(t, database), "worker-branches")
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Dir(branches))
	git, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "scale private Git", err)
	selector := workspace.HostSelector()
	selector.BudgetFor = adapter.ZeroBudget
	manager := workspace.NewManager(branches, selector).WithPrivateGit(git)
	queue := NewSQLQueue(database, 4)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(testbaseline.DataDir(t, database), "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(manager)
	return &layeredWorkerFixture{db: database, queue: queue, manager: manager, source: source, branches: branches}
}

func claimLargeSourceWorker(t *testing.T, f *layeredWorkerFixture, name string) *api.WorkerTask {
	t.Helper()
	id, err := f.queue.Enqueue(t.Context(), api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: f.source, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{name}},
		AgentType: "implementer", Prompt: "Create the scoped file", Brief: "Create the scoped file",
	})
	testutil.FailErr(t, "enqueue scale worker", err)
	next, err := f.queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim scale worker", err)
	if next == nil {
		t.Fatal("scale worker was not claimable")
	}
	task, err := f.queue.ClaimWorkerBranch(t.Context(), id)
	testutil.FailErr(t, "prepare scale worker", err)
	task.ClaimToken = next.ClaimToken
	return task
}

// largeSource commits a generated tree of files in nested directories, as
// one fast-import stream so building it costs no per-file Git process.
func largeSource(t *testing.T, files int) string {
	t.Helper()
	source := t.TempDir()
	gittest.Init(t, source)
	var stream bytes.Buffer
	stream.WriteString("blob\nmark :1\ndata 7\nsource\n")
	stream.WriteString("commit refs/heads/main\ncommitter Painted Wolf test <test@paintedwolf.dev> 0 +0000\ndata 6\nlarge\n")
	stream.WriteString("M 100644 :1 README.md\n")
	for i := range files {
		fmt.Fprintf(&stream, "M 100644 :1 d%03d/e%03d/f%06d.txt\n", i%1000, (i/1000)%100, i)
	}
	out, code, err := gitexec.Run(t.Context(), source, []string{"fast-import", "--quiet"},
		gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: stream.Bytes()})
	if err != nil || code != 0 {
		t.Fatalf("fast-import: exit %d: %v: %s", code, err, out)
	}
	gittest.Run(t, source, "checkout", "-f", "main")
	return source
}
