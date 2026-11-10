//go:build stress

package worker

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
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

// This explicitly exercises the mounted lane, not production budget selection.
// Restart, broad traversal and concurrent-worker scale are separate scenarios.
func TestStressFirefoxMountedSparseWorkerLifecycle(t *testing.T) {
	source := os.Getenv("PW_M14_FIREFOX_SOURCE")
	if source == "" {
		t.Skip("requires explicitly transferred pinned Firefox fixture")
	}
	const pin = "4a9cf9ae9564d9f3209a04b461670baa0b0c5f0e"
	if got := gittest.Run(t, source, "rev-parse", "HEAD"); strings.TrimSpace(got) != pin {
		t.Fatalf("unexpected Firefox fixture head: %q", got)
	}
	original, err := os.ReadFile(filepath.Join(source, "README.md"))
	testutil.FailErr(t, "source read oracle", err)
	name := "m14-test-" + rand.Text() + ".txt"
	content := []byte("isolated worker change: " + name + "\n")
	sourceFile := filepath.Join(source, name)
	if _, err := os.Lstat(sourceFile); !os.IsNotExist(err) {
		t.Fatalf("fixture path is not unused: %v", err)
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(sourceFile)
		if os.IsNotExist(err) {
			return
		}
		testutil.FailErr(t, "inspect authored fixture path", err)
		if !bytes.Equal(data, content) {
			t.Fatal("preserving unexpected source content at authored path")
		}
		testutil.FailErr(t, "remove only authored source file", os.Remove(sourceFile))
	})
	fixture := newFirefoxWorkerFixture(t, source)
	started := time.Now()
	task := claimFirefoxWorker(t, fixture, name)
	t.Logf("prepare elapsed=%s", time.Since(started))
	t.Cleanup(func() {
		testutil.FailErr(t, "destroy scale worker", fixture.manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
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
	if got := gittest.Run(t, source, "rev-parse", "HEAD"); strings.TrimSpace(got) != pin {
		t.Fatal("promotion changed source HEAD")
	}
}

func newFirefoxWorkerFixture(t *testing.T, source string) *layeredWorkerFixture {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", source)
	branches := filepath.Join(testbaseline.DataDir(t, database), "worker-branches")
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Dir(branches))
	git, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "scale private Git", err)
	t.Cleanup(func() { _ = git.Store().Close() })
	selector := workspace.EnforcedWorkspaceSelector()
	selector.BudgetFor = adapter.ZeroBudget
	manager := workspace.NewManager(branches).WithPrivateGit(git).WithSelector(selector)
	queue := NewSQLQueue(database, 4)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(testbaseline.DataDir(t, database), "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(manager)
	return &layeredWorkerFixture{db: database, queue: queue, manager: manager, source: source, branches: branches}
}

func claimFirefoxWorker(t *testing.T, f *layeredWorkerFixture, name string) *api.WorkerTask {
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
