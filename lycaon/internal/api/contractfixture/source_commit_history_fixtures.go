package contractfixture

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitstate"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func CommitsByHash(rows []wire.SourceFileCommit) map[string]wire.SourceFileCommit {
	out := make(map[string]wire.SourceFileCommit, len(rows))
	for _, row := range rows {
		out[row.Commit] = row
	}
	return out
}

// Merged history joins matching commits, arrivals, and retained versions.

type MergedHistoryFixture struct {
	Srv                                   *hostapi.Server
	ProjectID, RootID, FileID, Nested     string
	CommitInit, CommitA, CommitB, CommitC string
	TransitionID                          string
}

func (f MergedHistoryFixture) Listing(t *testing.T) wire.SourceFileVersionsResponse {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet,
		"/v1/projects/"+f.ProjectID+"/source/versions?file_id="+f.FileID, nil)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.SourceFileVersionsResponse
	testutil.FailErr(t, "decode merged listing", json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func NestedRootRepo(t *testing.T) (repo, nested string) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) {
		gittest.Run(t, repo, args...)
	}
	gittest.Init(t, repo)

	nested = filepath.Join(repo, "packages", "app")
	for _, dir := range []string{filepath.Join(repo, "src"), filepath.Join(nested, "src")} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write top-level file",
		os.WriteFile(filepath.Join(repo, "src", "app.ts"), []byte("TOP LEVEL\n"), 0o644))
	testutil.FailErr(t, "write nested file",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), []byte("committed\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "init")
	return repo, nested
}

// withTestGitManager serves a real git board and repo-set cache.

func NewMergedHistoryFixture(t *testing.T) MergedHistoryFixture {
	t.Helper()
	ledger, ledgerDB, withLedger := TestSourceLedger(t)
	srv := NewTestServer(t, WithTestGitManager, withLedger, WithoutSourceInventory, WithSessionStore(sessionstore.NewSQL(ledgerDB)))
	repo, nested := NestedRootRepo(t) // init commit: nested/src/app.ts "committed\n"

	p := CreateProjectForTest(t, srv, nested)
	MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	appPath := filepath.Join(nested, "src", "app.ts")

	gitAt := func(args ...string) string {
		t.Helper()
		return strings.TrimSpace(gittest.Run(t, repo, args...))
	}
	commitBytes := func(content, message string) string {
		t.Helper()
		testutil.FailErr(t, "write "+message, os.WriteFile(appPath, []byte(content), 0o644))
		gitAt("add", "-A")
		gitAt("commit", "-m", message)
		return gitAt("rev-parse", "HEAD")
	}

	commitInit := gitAt("rev-parse", "HEAD")
	commitA := commitBytes("second\n", "pre-tracking growth")

	trackedBytes := []byte("tracked v1\n")
	testutil.FailErr(t, "write tracked bytes", os.WriteFile(appPath, trackedBytes, 0o644))
	testutil.FailErr(t, "record tracked edit", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser,
		Before: []byte("second\n"), After: trackedBytes,
	}))
	reader := NewScriptedGitStateReader(t, nested,
		gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: commitA, HeadRef: "main"})
	ledger.SetGitReader(reader)
	_, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "seed git state", err)

	blobDir := t.TempDir()
	writeBlob := func(content string) string {
		t.Helper()
		staged := filepath.Join(blobDir, "blob")
		testutil.FailErr(t, "stage blob bytes", os.WriteFile(staged, []byte(content), 0o644))
		return gitAt("hash-object", "-w", staged)
	}
	commitBlob := func(parent, content, message string) string {
		t.Helper()
		blob := writeBlob(content)
		gitAt("read-tree", parent)
		gitAt("update-index", "--add", "--cacheinfo", "100644,"+blob+",packages/app/src/app.ts")
		tree := gitAt("write-tree")
		return gitAt("commit-tree", tree, "-p", parent, "-m", message)
	}
	commitB := commitBlob(commitA, "intermediate\n", "intermediate work")
	commitC := commitBlob(commitB, "landed\n", "landed work")
	gitAt("reset", "--hard", commitC)
	reader.Put(nested, gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: commitC, HeadRef: "main",
	})
	reader.PutLog(nested, []gitstate.RefLogEntry{
		{Commit: commitC, Subject: "commit: landed work"},
		{Commit: commitA, Subject: "commit: init"},
	})
	terminal, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "observe offline commits", err)
	if terminal[rootID] == "" {
		t.Fatalf("terminal transitions = %+v", terminal)
	}
	testutil.FailErr(t, "record reconcile", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginExternal,
		Cause:  "filesystem_reconcile", CaptureQuality: "reconciled",
		Before: trackedBytes, After: []byte("landed\n"),
		GitTransitionID: terminal[rootID],
	}))
	fileID, _, err := ledger.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
	testutil.FailErr(t, "resolve file", err)

	return MergedHistoryFixture{
		Srv: srv, ProjectID: p.ID, RootID: rootID, FileID: fileID, Nested: nested,
		CommitInit: commitInit, CommitA: commitA, CommitB: commitB, CommitC: commitC,
		TransitionID: terminal[rootID],
	}
}

func NewScriptedGitStateReader(t *testing.T, rootAbs string, state gitstate.State) *ScriptedGitStateReader {
	t.Helper()
	r := &ScriptedGitStateReader{T: t, Logs: map[string][]gitstate.RefLogEntry{}}
	r.Put(rootAbs, state)
	return r
}

func ReadCommitReview(t *testing.T, srv *hostapi.Server, projectID, cursor string, limit string) wire.SourceWalkResponse {
	t.Helper()
	q := url.Values{"baseline": {"commit"}, "limit": {limit}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, NewAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/walk?"+q.Encode(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("review status=%d body=%s", w.Code, w.Body.String())
	}
	var response wire.SourceWalkResponse
	testutil.FailErr(t, "decode commit review", json.Unmarshal(w.Body.Bytes(), &response))
	return response
}

func ResolvedRootKey(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

type ScriptedGitStateReader struct {
	T      *testing.T
	States map[string]gitstate.State
	Logs   map[string][]gitstate.RefLogEntry
}

// newScriptedGitStateReader indexes states by canonical root path.

func (f *ScriptedGitStateReader) PutLog(rootAbs string, entries []gitstate.RefLogEntry) {
	if f.Logs == nil {
		f.Logs = map[string][]gitstate.RefLogEntry{}
	}
	f.Logs[ResolvedRootKey(rootAbs)] = entries
}

// resolvedRootKey canonicalizes symlinked roots.

func (f *ScriptedGitStateReader) Put(rootAbs string, state gitstate.State) {
	if f.States == nil {
		f.States = map[string]gitstate.State{}
	}
	f.States[ResolvedRootKey(rootAbs)] = state
}

func (f *ScriptedGitStateReader) HeadState(_ context.Context, rootAbs string) gitstate.State {
	state, ok := f.States[ResolvedRootKey(rootAbs)]
	if !ok {
		f.T.Errorf("scripted git reader has no state for %q (known: %v)",
			rootAbs, slices.Sorted(maps.Keys(f.States)))
		return gitstate.State{Repo: gitstate.RepoAbsent}
	}
	return state
}

func (f *ScriptedGitStateReader) RefLogHead(_ context.Context, rootAbs string, _ int) ([]gitstate.RefLogEntry, error) {
	return f.Logs[ResolvedRootKey(rootAbs)], nil
}

// Git-caused versions restore from repository objects without retained blobs.

func WithTestGitManager(d *hostapi.Dependencies) {
	manager := git.NewManager()
	d.Workflow.Board = &board.SnapshotBuilder{Git: manager, StatusCache: git.NewStatusCache(manager)}
	d.Workflow.RepoSetCache = git.NewRepoSetCache(git.DefaultStatusCacheTTL)
}

// Nested roots resolve paths from their attachment point.

func WithoutSourceInventory(d *hostapi.Dependencies) { d.Source.SourceInventory = nil }
