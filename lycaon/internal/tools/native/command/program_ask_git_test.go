package command_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/projectroot"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskHermeticGitReadsLinkedWorktreeUnbornBranchAndManagedView(t *testing.T) {
	gittestsetup.Enable()
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)

	// 1. Unborn branch repository: git status and git branch --show-current succeed in Ask mode.
	unborn := newAskComposedFixture(t)
	runGitCLI(t, bin, unborn.Dir, "init", "-q", "-b", "unborn-main")
	testutil.FailErr(t, "seed untracked", os.WriteFile(filepath.Join(unborn.Dir, "draft.txt"), []byte("draft\n"), 0o644))
	unbornRaw, err := unborn.Call(t, t.Context(), "git status -sb; git branch --show-current")
	testutil.FailErr(t, "unborn git reads", err)
	unbornOut := decodeOutcome(t, unbornRaw)
	if unbornOut.Status != 0 || unbornOut.Refusal != nil || !strings.Contains(unbornOut.Stdout, "unborn-main") {
		t.Fatalf("unborn outcome = %+v", unbornOut)
	}

	// 2. Linked worktree confinement fixture: verify BindAgentLaunch policy and byte-for-byte unchanged
	// resolved worktree Git directory and shared common Git directory.
	mainRepo := fspath.CanonicalPath(t.TempDir())
	runGitCLI(t, bin, mainRepo, "init", "-q", "-b", "main")
	testutil.FailErr(t, "write tracked.txt", os.WriteFile(filepath.Join(mainRepo, "tracked.txt"), []byte("line1\n"), 0o644))
	runGitCLI(t, bin, mainRepo, "add", "tracked.txt")
	runGitCLI(t, bin, mainRepo, "commit", "-q", "-m", "initial commit")

	linkedParent := fspath.CanonicalPath(t.TempDir())
	linkedWT := filepath.Join(linkedParent, "linked-wt")
	runGitCLI(t, bin, mainRepo, "worktree", "add", "-q", "-b", "linked-branch", linkedWT, "HEAD")

	testutil.FailErr(t, "modify tracked.txt", os.WriteFile(filepath.Join(linkedWT, "tracked.txt"), []byte("line1\nline2\n"), 0o644))
	testutil.FailErr(t, "add untracked.txt", os.WriteFile(filepath.Join(linkedWT, "untracked.txt"), []byte("new\n"), 0o644))

	wtGitDir := resolveGitPath(t, bin, linkedWT, "--git-dir")
	commonGitDir := resolveGitPath(t, bin, linkedWT, "--git-common-dir")
	if wtGitDir == commonGitDir || !strings.HasPrefix(wtGitDir, commonGitDir) {
		t.Fatalf("expected linked worktree git-dir %q distinct from common-dir %q", wtGitDir, commonGitDir)
	}

	fLinked := newAskComposedFixture(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	_, err = confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start egress broker", err)
	var egressMu sync.Mutex
	var egressAttempts []string
	confine.SetEgressPosture(confine.PostureAsk)
	releaseEgress := confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		egressMu.Lock()
		egressAttempts = append(egressAttempts, fmt.Sprintf("%s:%d", ep.Host, ep.Port))
		egressMu.Unlock()
		return false
	})
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		_ = releaseEgress(context.Background())
		_ = confine.StopEgressBroker()
	})
	fLinked.Context = func(_ string, tc *tools.ToolContext) {
		tc.Source.Roots = []projectroot.RootRef{
			{ID: "r1", Label: "root", Path: linkedWT, IsPrimary: true},
			{ID: "r2", Label: "common", Path: mainRepo},
		}
		tc.Source.ActiveRootID = "r1"
	}

	wtBefore := snapshotTreeBytes(t, wtGitDir)
	commonBefore := snapshotTreeBytes(t, commonGitDir)
	fLinked.resetLaunches()

	gitProgram := "git status -sb; git diff -- tracked.txt; git log -n 1 --format=%s; git rev-parse --verify HEAD; git branch --show-current"
	linkedRaw, err := fLinked.Call(t, t.Context(), gitProgram)
	testutil.FailErr(t, "linked worktree git reads in Ask", err)
	linkedOut := decodeOutcome(t, linkedRaw)
	if linkedOut.Status != 0 || linkedOut.Refusal != nil {
		t.Fatalf("linked worktree outcome = %+v", linkedOut)
	}
	for _, want := range []string{"## linked-branch", "+line2", "initial commit", "linked-branch"} {
		if !strings.Contains(linkedOut.Stdout, want) {
			t.Fatalf("linked worktree stdout missing %q:\n%s", want, linkedOut.Stdout)
		}
	}

	wtAfter := snapshotTreeBytes(t, wtGitDir)
	commonAfter := snapshotTreeBytes(t, commonGitDir)
	assertTreeSnapshotEqual(t, "worktree git dir", wtBefore, wtAfter)
	assertTreeSnapshotEqual(t, "shared common git dir", commonBefore, commonAfter)

	launches := fLinked.recordedLaunches()
	if len(launches) == 0 {
		t.Fatal("expected BindAgentLaunch to be invoked for hermetic Git reads")
	}
	wantScratch := fspath.CanonicalPath(fLinked.ScratchDir)
	for i, plan := range launches {
		conf := plan.Confinement
		if conf == nil {
			t.Fatalf("launch[%d] missing confinement", i)
		}
		if conf.Network != confine.NetworkProxyOnly {
			t.Fatalf("launch[%d] network = %v, want NetworkProxyOnly", i, conf.Network)
		}
		if len(conf.DirectIPPermits) != 0 || conf.SocksProxyEnv {
			t.Fatalf("launch[%d] unexpected egress grants: directIP=%v socks=%t", i, conf.DirectIPPermits, conf.SocksProxyEnv)
		}
		if len(conf.GrantedWriteRoots) != 0 || len(conf.Roots) != 1 || fspath.CanonicalPath(conf.Roots[0]) != wantScratch {
			t.Fatalf("launch[%d] write roots = %v (granted=%v), want only scratch %q", i, conf.Roots, conf.GrantedWriteRoots, wantScratch)
		}
	}
	egressMu.Lock()
	gotEgress := slices.Clone(egressAttempts)
	egressMu.Unlock()
	if len(gotEgress) != 0 {
		t.Fatalf("unexpected remote egress attempts during Git reads: %v", gotEgress)
	}

	// 3. Reviewer managed privategit view: private view preparation touches only private view state,
	// leaving the protected source repository and promoted overlay unchanged.
	sourceRepo := fspath.CanonicalPath(t.TempDir())
	runGitCLI(t, bin, sourceRepo, "init", "-q", "-b", "main")
	testutil.FailErr(t, "seed source file", os.WriteFile(filepath.Join(sourceRepo, "app.go"), []byte("package app\n"), 0o644))
	runGitCLI(t, bin, sourceRepo, "add", "app.go")
	runGitCLI(t, bin, sourceRepo, "commit", "-q", "-m", "source commit")

	promotedOverlay := fspath.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "seed promoted overlay", os.WriteFile(filepath.Join(promotedOverlay, "promoted.json"), []byte(`{"promoted":true}`+"\n"), 0o644))

	sourceBefore := snapshotTreeBytes(t, sourceRepo)
	overlayBefore := snapshotTreeBytes(t, promotedOverlay)

	viewWorktree := fspath.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "seed materialized view file", os.WriteFile(filepath.Join(viewWorktree, "app.go"), []byte("package app\n"), 0o644))
	pgitBacking := fspath.CanonicalPath(t.TempDir())
	coreBacking := fspath.CanonicalPath(t.TempDir())
	mgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: pgitBacking})
	testutil.FailErr(t, "new privategit manager", err)
	const viewID, workerID = "view-reviewer-1", "reviewer-1"
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:         viewID,
		BackingDir:     coreBacking,
		GitInterceptor: mgr,
		Roots:          []layeredfs.RootSpec{{ID: "r1", Label: "root", TrunkPath: viewWorktree, IsPrimary: true}},
	})
	testutil.FailErr(t, "new layeredfs core", err)
	t.Cleanup(func() {
		_ = mgr.ReleaseView(viewID)
		_ = core.ReleaseView()
	})
	testutil.FailErr(t, "adopt materialized reviewer view", mgr.AdoptMaterializedView(t.Context(), privategit.MaterializedViewBinding{
		ViewID: viewID, WorkerID: workerID, RootID: "r1",
		TrunkDir: sourceRepo, WorktreeDir: viewWorktree, Core: core,
	}))

	fManaged := newAskComposedFixture(t)
	fManaged.privateGit = mgr
	fManaged.Context = func(_ string, tc *tools.ToolContext) {
		tc.Source.Roots = []projectroot.RootRef{{ID: "r1", Label: "root", Path: viewWorktree, IsPrimary: true}}
		tc.Source.ActiveRootID = "r1"
		tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
		tc.Identity.WorkerJobID = workerID
		tc.Source.WorkerBranchRoot = viewWorktree
		tc.Source.BranchWorkspace = askWorkerBranch{}
	}

	managedRaw, err := fManaged.Call(t, t.Context(), "git status -sb; git log -n 1 --format=%s")
	testutil.FailErr(t, "managed view git reads in Ask", err)
	managedOut := decodeOutcome(t, managedRaw)
	if managedOut.Status != 0 || managedOut.Refusal != nil || !strings.Contains(managedOut.Stdout, "source commit") {
		t.Fatalf("managed view outcome = %+v", managedOut)
	}
	metrics, ok := mgr.RepoMetrics(viewID, ".")
	if !ok || metrics.State != privategit.StateReady {
		t.Fatalf("expected private view repo StateReady, got %+v (ok=%t)", metrics, ok)
	}
	changes, err := core.EnumerateChanges(true)
	testutil.FailErr(t, "enumerate core changes", err)
	if len(changes) != 0 {
		t.Fatalf("private view preparation manufactured overlay changes: %+v", changes)
	}
	assertTreeSnapshotEqual(t, "protected source repository", sourceBefore, snapshotTreeBytes(t, sourceRepo))
	assertTreeSnapshotEqual(t, "promoted overlay", overlayBefore, snapshotTreeBytes(t, promotedOverlay))
}

type askWorkerBranch struct{}

func (askWorkerBranch) ValidateMeta(context.Context) error { return nil }

func (askWorkerBranch) EnsureParents(context.Context, string) error { return nil }

func runGitCLI(t *testing.T, bin, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Tester",
		"GIT_AUTHOR_EMAIL=tester@example.test",
		"GIT_COMMITTER_NAME=Tester",
		"GIT_COMMITTER_EMAIL=tester@example.test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func resolveGitPath(t *testing.T, bin, dir, flag string) string {
	t.Helper()
	cmd := exec.Command(bin, "rev-parse", flag)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v (%s)", flag, err, out)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return fspath.CanonicalPath(p)
}

func snapshotTreeBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			snap[rel] = fmt.Sprintf("dir:%o", info.Mode().Perm())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		snap[rel] = fmt.Sprintf("file:%o:%d:%s", info.Mode().Perm(), len(data), hex.EncodeToString(sum[:]))
		return nil
	})
	testutil.FailErr(t, "snapshot tree "+root, err)
	return snap
}

func assertTreeSnapshotEqual(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s entry count changed: before=%d after=%d", label, len(before), len(after))
	}
	for k, v := range before {
		if got, ok := after[k]; !ok || got != v {
			t.Fatalf("%s entry %q changed: before=%q after=%q", label, k, v, got)
		}
	}
}
