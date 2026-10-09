package session

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func initReachRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "write README", os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))
	gittest.InitCommit(t, dir, "init")
	abs, err := filepath.EvalSymlinks(dir)
	if err != nil {
		abs, err = filepath.Abs(dir)
		testutil.FailErr(t, "abs repo", err)
	}
	return abs
}

type reachFixture struct {
	mgr     *Manager
	mem     *store.Memory
	reg     *project.MemoryRegistry
	project *project.Project
	sess    *api.Session
	repoDir string
	wtPath  string
	gm      *git.Manager
}

func newReachFixture(t *testing.T, rootPath string) *reachFixture {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	mem := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	mgr := NewManager(mem, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, rootPath)
	testutil.FailErr(t, "CreateWithRoot", err)
	mgr.SetProjectRegistry(reg)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session", err)
	return &reachFixture{
		mgr:     mgr,
		mem:     mem,
		reg:     reg,
		project: p,
		sess:    sess,
		repoDir: rootPath,
		gm:      git.NewManager(),
	}
}

func (f *reachFixture) bind(t *testing.T) {
	t.Helper()
	base, err := f.gm.Branch(t.Context(), f.repoDir)
	testutil.FailErr(t, "base branch", err)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval wt parent", err)
	wt := filepath.Join(parent, "wt-"+f.sess.ID)
	branch := "session/" + f.sess.ID
	testutil.FailErr(t, "AddWorktree", f.gm.AddWorktree(t.Context(), f.repoDir, wt, branch, base))
	f.wtPath = wt
	testutil.FailErr(t, "PutWorktreeBinding", f.mem.PutWorktreeBinding(t.Context(), store.WorktreeBinding{
		SessionID:    f.sess.ID,
		ProjectID:    f.project.ID,
		RepoID:       "r-test",
		Toplevel:     f.repoDir,
		WorktreePath: wt,
		Branch:       branch,
		BaseBranch:   base,
	}))
}

func sameReachPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

func writeBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{
		{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"write": true, "command": true}},
	})
}

func TestWorktreeReach_writeLandsInWorktree(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	f.bind(t)
	tctx, err := f.mgr.buildToolContext(t.Context(), f.sess, toolprofiles.DefaultToolProfileID, inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)

	tool := &native.WriteTool{Boundary: writeBoundary(t)}
	_, err = tool.Run(t.Context(), map[string]any{
		"path":    "reach-marker.txt",
		"content": "from-agent\n",
	}, tctx)
	testutil.FailErr(t, "write", err)

	wtFile := filepath.Join(f.wtPath, "reach-marker.txt")
	if _, err := os.Stat(wtFile); err != nil {
		t.Fatalf("expected write under worktree: %v", err)
	}
	projFile := filepath.Join(f.repoDir, "reach-marker.txt")
	if _, err := os.Stat(projFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project root must stay clean, got %v", err)
	}
	out := gittest.Run(t, f.repoDir, "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("user folder dirty: %q", out)
	}
}

func TestWorktreeReach_jailFollows(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	f.bind(t)
	tctx, err := f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)

	host := tools.HostWriteRoot(tctx)
	if !sameReachPath(host, f.wtPath) {
		t.Fatalf("HostWriteRoot = %q want worktree %q", host, f.wtPath)
	}
	jail := tools.ConfineRootsForAction(tctx)
	foundWT, foundProj := false, false
	for _, p := range jail {
		if sameReachPath(p, f.wtPath) {
			foundWT = true
		}
		if sameReachPath(p, f.repoDir) {
			foundProj = true
		}
	}
	if !foundWT {
		t.Fatalf("jail missing worktree: %v", jail)
	}
	if foundProj {
		t.Fatalf("jail must not include project root: %v", jail)
	}
}

func TestWorktreeReach_commandCwdFollows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	f := newReachFixture(t, initReachRepo(t))
	f.bind(t)
	testutil.FailErr(t, "wt marker", os.WriteFile(filepath.Join(f.wtPath, "marker"), []byte("worktree"), 0o644))
	testutil.FailErr(t, "proj marker", os.WriteFile(filepath.Join(f.repoDir, "marker"), []byte("project"), 0o644))

	tctx, err := f.mgr.buildToolContext(t.Context(), f.sess, toolprofiles.DefaultToolProfileID, inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)

	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	tool := &native.CommandTool{
		Runner:     hostcmd.NewRunner(),
		Boundary:   writeBoundary(t),
		Background: reg,
	}
	out, err := tool.Run(t.Context(), map[string]any{"command": "cat marker"}, tctx)
	testutil.FailErr(t, "command", err)
	if !strings.Contains(out, "worktree") {
		t.Fatalf("command cwd output = %q want worktree marker", out)
	}
}

func TestWorktreeReach_rootIdentityStable(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	unbound, err := f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	testutil.FailErr(t, "unbound context", err)
	activeID := unbound.ActiveRootID
	if activeID == "" {
		t.Fatal("expected active root id")
	}
	f.bind(t)
	bound, err := f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	testutil.FailErr(t, "bound context", err)
	if bound.ActiveRootID != activeID {
		t.Fatalf("ActiveRootID changed: %q -> %q", activeID, bound.ActiveRootID)
	}
	if !sameReachPath(bound.ActiveRootPath(), f.wtPath) {
		t.Fatalf("ActiveRootPath = %q want %q", bound.ActiveRootPath(), f.wtPath)
	}
	if sameReachPath(unbound.ActiveRootPath(), bound.ActiveRootPath()) {
		t.Fatal("path should move after bind")
	}
}

func TestWorktreeReach_foreignRootsUntouched(t *testing.T) {
	primary := initReachRepo(t)
	foreign := initReachRepo(t)
	f := newReachFixture(t, primary)
	_, err := f.reg.AttachRoot(t.Context(), f.project.ID, project.AttachRootParams{Path: foreign})
	testutil.FailErr(t, "AttachRoot", err)
	p, err := f.reg.Get(t.Context(), f.project.ID)
	testutil.FailErr(t, "reload project", err)
	f.project = p
	f.bind(t)

	tctx, err := f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)
	if len(tctx.Roots) != 2 {
		t.Fatalf("roots = %d want 2", len(tctx.Roots))
	}
	var foreignPath string
	for _, r := range tctx.Roots {
		if sameReachPath(r.Path, f.wtPath) {
			continue
		}
		foreignPath = r.Path
	}
	if !sameReachPath(foreignPath, foreign) {
		t.Fatalf("foreign root = %q want %q", foreignPath, foreign)
	}
}

func TestWorktreeReach_rootBelowToplevel(t *testing.T) {
	top := initReachRepo(t)
	web := filepath.Join(top, "packages", "web")
	testutil.FailErr(t, "mkdir web", os.MkdirAll(web, 0o755))
	testutil.FailErr(t, "write pkg", os.WriteFile(filepath.Join(web, "app.js"), []byte("1\n"), 0o644))

	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	mem := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	mgr := NewManager(mem, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, web)
	testutil.FailErr(t, "CreateWithRoot", err)
	mgr.SetProjectRegistry(reg)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{
		Posture: api.SessionPostureBuild, ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create", err)

	gm := git.NewManager()
	base, err := gm.Branch(t.Context(), top)
	testutil.FailErr(t, "branch", err)
	wt := filepath.Join(t.TempDir(), "wt")
	testutil.FailErr(t, "AddWorktree", gm.AddWorktree(t.Context(), top, wt, "session/"+sess.ID, base))
	testutil.FailErr(t, "bind", mem.PutWorktreeBinding(t.Context(), store.WorktreeBinding{
		SessionID: sess.ID, ProjectID: p.ID, RepoID: "r",
		Toplevel: top, WorktreePath: wt, Branch: "session/" + sess.ID, BaseBranch: base,
	}))

	tctx, err := mgr.buildToolContext(t.Context(), sess, "", inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)
	want := filepath.Join(wt, "packages", "web")
	if !sameReachPath(tctx.ActiveRootPath(), want) {
		t.Fatalf("ActiveRootPath = %q want %q", tctx.ActiveRootPath(), want)
	}
}

func TestWorktreeReach_workerInheritance(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	f.bind(t)
	marker := "worker-src-only.txt"
	testutil.FailErr(t, "wt-only file", os.WriteFile(filepath.Join(f.wtPath, marker), []byte("from-wt\n"), 0o644))

	tctx, err := f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)
	if !sameReachPath(tctx.Roots[0].Path, f.wtPath) {
		t.Fatalf("substituted root = %q want %q", tctx.Roots[0].Path, f.wtPath)
	}
	child, err := f.mem.CreateChild(t.Context(), f.sess, api.SpawnChildRequest{AgentType: "implementer", Prompt: "edit"})
	testutil.FailErr(t, "CreateChild", err)
	childCtx, err := f.mgr.buildToolContext(t.Context(), child, "", inject.Machine{})
	testutil.FailErr(t, "build child ToolContext", err)
	if !sameReachPath(childCtx.Roots[0].Path, f.wtPath) {
		t.Fatalf("child root = %q want inherited worktree %q", childCtx.Roots[0].Path, f.wtPath)
	}

	ws := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds"))
	binding, layout, err := ws.CreateWorkerWorkspaceFromSources(t.Context(), tctx.Roots, tctx.Roots, tctx.ActiveRootID, "job-reach")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	if !sameReachPath(layout.Roots[0].Path, f.wtPath) {
		t.Fatalf("overlay source = %q want worktree", layout.Roots[0].Path)
	}
	copied := filepath.Join(binding.Root, marker)
	data, err := os.ReadFile(copied)
	testutil.FailErr(t, "read overlay copy", err)
	if string(data) != "from-wt\n" {
		t.Fatalf("overlay content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(f.repoDir, marker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker must not exist in user folder")
	}
}

func TestWorktreeReach_staleRefusesTurn(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	f.bind(t)
	testutil.FailErr(t, "remove worktree dir", os.RemoveAll(f.wtPath))

	_, err := f.mgr.Prompt(t.Context(), f.sess.ID, "hello")
	if !errors.Is(err, ErrSessionWorktreeStale) {
		t.Fatalf("Prompt = %v want ErrSessionWorktreeStale", err)
	}
	_, err = f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	if !errors.Is(err, ErrSessionWorktreeStale) {
		t.Fatalf("buildToolContext = %v want ErrSessionWorktreeStale", err)
	}
}

type getFailStore struct {
	Store
	failGet bool
}

func (s *getFailStore) GetWorktreeBinding(ctx context.Context, sessionID string) (*store.WorktreeBinding, bool, error) {
	if s.failGet {
		return nil, false, errors.New("injected get failure")
	}
	return s.Store.GetWorktreeBinding(ctx, sessionID)
}

func TestWorktreeReach_getFailureRefuses(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	repoDir := initReachRepo(t)
	mem := store.NewMemory()
	failing := &getFailStore{Store: mem, failGet: true}
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	mgr := NewManager(failing, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, repoDir)
	testutil.FailErr(t, "CreateWithRoot", err)
	mgr.SetProjectRegistry(reg)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.buildToolContext(t.Context(), sess, "", inject.Machine{})
	if err == nil {
		t.Fatal("buildToolContext must refuse on binding-store Get failure")
	}
	if errors.Is(err, ErrSessionWorktreeStale) {
		t.Fatal("Get failure must not be remapped to stale")
	}
	_, err = mgr.Prompt(t.Context(), sess.ID, "hello")
	if err == nil {
		t.Fatal("Prompt must refuse on binding-store Get failure")
	}
	// The refusals above fail closed; project roots apply only once Get succeeds.
	failing.failGet = false
	tctx, err := mgr.buildToolContext(t.Context(), sess, "", inject.Machine{})
	testutil.FailErr(t, "buildToolContext unbound", err)
	if !sameReachPath(tctx.ActiveRootPath(), repoDir) {
		t.Fatalf("unbound path = %q want %q", tctx.ActiveRootPath(), repoDir)
	}
}

func TestWorktreeReach_unboundUnchanged(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	tctx, err := f.mgr.buildToolContext(t.Context(), f.sess, "", inject.Machine{})
	testutil.FailErr(t, "buildToolContext", err)
	if !sameReachPath(tctx.ActiveRootPath(), f.repoDir) {
		t.Fatalf("unbound path = %q want %q", tctx.ActiveRootPath(), f.repoDir)
	}
	jail := tools.ConfineRootsForAction(tctx)
	found := false
	for _, p := range jail {
		if sameReachPath(p, f.repoDir) {
			found = true
		}
	}
	if !found {
		t.Fatalf("unbound jail missing project root: %v", jail)
	}
}

func TestWorktreeReach_boardWorktreeFact(t *testing.T) {
	f := newReachFixture(t, initReachRepo(t))
	f.bind(t)
	fn := f.mgr.BoardGitWorktreeFunc(f.gm)
	fact := fn(t.Context(), f.sess.ID)
	if fact == nil {
		t.Fatal("expected worktree fact")
	}
	if fact.Branch != "session/"+f.sess.ID || fact.BaseBranch == "" {
		t.Fatalf("fact = %#v", fact)
	}
	if fn(t.Context(), "") != nil {
		t.Fatal("empty session id must be nil")
	}
}

func TestSubstituteWorktreeRoots_hasDeclaredBoundaries(t *testing.T) {
	root := filepath.Join("..", "..") // lycaon/
	var callers []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == "vendor" || base == "testdata" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if !strings.Contains(string(src), "SubstituteWorktreeRoots") {
			return nil
		}
		// Definition site is not a caller.
		if strings.Contains(path, filepath.Join("internal", "project", "worktree_binding.go")) {
			return nil
		}
		callers = append(callers, path)
		_ = file
		return nil
	})
	testutil.FailErr(t, "walk", err)
	if len(callers) != 4 {
		t.Fatalf("SubstituteWorktreeRoots callers = %v want exactly 4", callers)
	}
	joined := strings.Join(callers, "\n")
	if !strings.Contains(joined, "tool_context.go") || !strings.Contains(joined, filepath.Join("requestscope", "session.go")) || !strings.Contains(joined, "navigation_refs.go") || !strings.Contains(joined, "workspace.go") {
		t.Fatalf("unexpected callers: %v", callers)
	}
}
