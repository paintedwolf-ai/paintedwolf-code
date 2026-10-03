package api

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/board"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitstate"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// nestedRootRepo duplicates a path across repository and attached roots.
func nestedRootRepo(t *testing.T) (repo, nested string) {
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
func withTestGitManager(d *Dependencies) {
	manager := git.NewManager()
	d.Board = &board.SnapshotBuilder{Git: manager, StatusCache: git.NewStatusCache(manager)}
	d.RepoSetCache = git.NewRepoSetCache(git.DefaultStatusCacheTTL)
}

// Nested roots resolve paths from their attachment point.
func TestCommitBaselineReadsTheNestedRootsOwnFile(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	_, nested := nestedRootRepo(t)

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "edit working file",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), []byte("working\n"), 0o644))
	testutil.FailErr(t, "record edit", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts",
		Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginUser,
		Before: []byte("committed\n"), After: []byte("working\n"),
	}))

	code, comparison := getSourceComparison(t, srv, p.ID, url.Values{
		"root_id":  {rootID},
		"path":     {"src/app.ts"},
		"baseline": {"commit"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if comparison.Before == nil || comparison.After == nil {
		t.Fatal("commit comparison has no endpoints")
	}
	if comparison.Before.Content != "committed\n" {
		t.Fatalf("before content = %q want the nested root's committed bytes", comparison.Before.Content)
	}
	// Only the attached root may supply baseline bytes.
	if comparison.Before.Content == "TOP LEVEL\n" {
		t.Fatal("commit baseline served the repository root's file")
	}
	if comparison.Before.VersionID != "" {
		t.Fatalf("before version id = %q; the commit is not a retained version",
			comparison.Before.VersionID)
	}
	if comparison.After.Content != "working\n" {
		t.Fatalf("after content = %q want the tracked head", comparison.After.Content)
	}
}

// scriptedGitStateReader serves scripted positions and reflogs per root path.
type scriptedGitStateReader struct {
	t      *testing.T
	states map[string]gitstate.State
	logs   map[string][]gitstate.RefLogEntry
}

// newScriptedGitStateReader indexes states by canonical root path.
func newScriptedGitStateReader(t *testing.T, rootAbs string, state gitstate.State) *scriptedGitStateReader {
	t.Helper()
	r := &scriptedGitStateReader{t: t, logs: map[string][]gitstate.RefLogEntry{}}
	r.put(rootAbs, state)
	return r
}

func (f *scriptedGitStateReader) putLog(rootAbs string, entries []gitstate.RefLogEntry) {
	if f.logs == nil {
		f.logs = map[string][]gitstate.RefLogEntry{}
	}
	f.logs[resolvedRootKey(rootAbs)] = entries
}

// resolvedRootKey canonicalizes symlinked roots.
func resolvedRootKey(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func (f *scriptedGitStateReader) put(rootAbs string, state gitstate.State) {
	if f.states == nil {
		f.states = map[string]gitstate.State{}
	}
	f.states[resolvedRootKey(rootAbs)] = state
}

func (f *scriptedGitStateReader) HeadState(_ context.Context, rootAbs string) gitstate.State {
	state, ok := f.states[resolvedRootKey(rootAbs)]
	if !ok {
		f.t.Errorf("scripted git reader has no state for %q (known: %v)",
			rootAbs, slices.Sorted(maps.Keys(f.states)))
		return gitstate.State{Repo: gitstate.RepoAbsent}
	}
	return state
}

func (f *scriptedGitStateReader) RefLogHead(_ context.Context, rootAbs string, _ int) ([]gitstate.RefLogEntry, error) {
	return f.logs[resolvedRootKey(rootAbs)], nil
}

// Git-caused versions restore from repository objects without retained blobs.
func TestRestoreGitCausedVersionUsesGitObjectStore(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger, withoutSourceInventory, withSessionStore(sessionstore.NewSQL(ledgerDB)))
	repo, nested := nestedRootRepo(t)

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	revParse := func() string {
		cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "HEAD")
		cmd.Env = lyexec.LocalGitEnv()
		out, err := cmd.CombinedOutput()
		testutil.FailErr(t, "git rev-parse", err)
		return strings.TrimSpace(string(out))
	}
	commitAll := func(message string) {
		gittest.CommitAll(t, repo, message)
	}

	first := revParse()
	reader := newScriptedGitStateReader(t, nested,
		gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: first, HeadRef: "main"})
	ledger.SetGitReader(reader)
	_, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "seed git state", err)

	gitBytes := []byte("from git\n")
	testutil.FailErr(t, "write committed bytes",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), gitBytes, 0o644))
	commitAll("rewrite from git")
	second := revParse()
	reader.put(nested, gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: second, HeadRef: "main",
	})
	reader.putLog(nested, []gitstate.RefLogEntry{
		{Commit: second, Subject: "commit: rewrite from git"},
		{Commit: first, Subject: "commit: init"},
	})
	terminal, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "observe commit", err)
	if terminal[rootID] == "" {
		t.Fatalf("terminal transitions = %+v", terminal)
	}

	testutil.FailErr(t, "record git-caused state", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginExternal,
		Cause:  "filesystem_reconcile", CaptureQuality: "reconciled",
		AfterSHA256: textfile.SHA256(gitBytes), AfterSize: int64(len(gitBytes)),
		GitTransitionID: terminal[rootID],
	}))
	fileID, gitVersionID, err := ledger.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
	testutil.FailErr(t, "resolve git-caused version", err)

	workingBytes := []byte("working edit\n")
	testutil.FailErr(t, "write working bytes",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), workingBytes, 0o644))
	testutil.FailErr(t, "record working edit", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser,
		Before: gitBytes, After: workingBytes,
	}))

	body, err := json.Marshal(wire.SourceVersionRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID, Path: "src/app.ts",
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256(workingBytes)},
	})
	testutil.FailErr(t, "marshal restore request", err)
	req := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/source/versions/"+gitVersionID+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response wire.SourceVersionRestoreResponse
	testutil.FailErr(t, "decode restore response", json.Unmarshal(w.Body.Bytes(), &response))
	if !response.Changed || response.Sha256 != textfile.SHA256(gitBytes) {
		t.Fatalf("restore response = %+v", response)
	}
	restored, err := os.ReadFile(filepath.Join(nested, "src", "app.ts"))
	testutil.FailErr(t, "read restored source", err)
	if string(restored) != string(gitBytes) {
		t.Fatalf("restored source = %q want the committed bytes", restored)
	}
}

// Git restore refuses bytes that do not match the recorded hash.
func TestRestoreGitCausedVersionRefusesDriftedGitBytes(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger, withoutSourceInventory)
	_, nested := nestedRootRepo(t)

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	reader := newScriptedGitStateReader(t, nested,
		gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "0000", HeadRef: "main"})
	ledger.SetGitReader(reader)
	_, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "seed git state", err)
	reader.put(nested, gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "1111", HeadRef: "main",
	})
	reader.putLog(nested, []gitstate.RefLogEntry{
		{Commit: "1111", Subject: "commit: claimed"},
		{Commit: "0000", Subject: "commit: earlier"},
	})
	terminal, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "observe commit", err)
	if terminal[rootID] == "" {
		t.Fatalf("terminal transitions = %+v", terminal)
	}

	// Point the record at unavailable bytes.
	testutil.FailErr(t, "record drifted state", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginExternal,
		Cause:  "filesystem_reconcile", CaptureQuality: "reconciled",
		AfterSHA256: strings.Repeat("a", 64), AfterSize: 9,
		GitTransitionID: terminal[rootID],
	}))
	fileID, versionID, err := ledger.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
	testutil.FailErr(t, "resolve drifted version", err)

	body, err := json.Marshal(wire.SourceVersionRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID, Path: "src/app.ts",
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("committed\n"))},
	})
	testutil.FailErr(t, "marshal restore request", err)
	req := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/source/versions/"+versionID+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// mergedHistoryFixture spans commit-only and retained history.
type mergedHistoryFixture struct {
	srv                                   *Server
	projectID, rootID, fileID, nested     string
	commitInit, commitA, commitB, commitC string
	transitionID                          string
}

func newMergedHistoryFixture(t *testing.T) mergedHistoryFixture {
	t.Helper()
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger, withoutSourceInventory, withSessionStore(sessionstore.NewSQL(ledgerDB)))
	repo, nested := nestedRootRepo(t) // init commit: nested/src/app.ts "committed\n"

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
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
	reader := newScriptedGitStateReader(t, nested,
		gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: commitA, HeadRef: "main"})
	ledger.SetGitReader(reader)
	_, err := ledger.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "seed git state", err)

	writeBlob := func(content string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "hash-object", "-w", "--stdin")
		cmd.Env = lyexec.LocalGitEnv()
		cmd.Stdin = strings.NewReader(content)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git hash-object: %v", err)
		}
		return strings.TrimSpace(string(out))
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
	reader.put(nested, gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: commitC, HeadRef: "main",
	})
	reader.putLog(nested, []gitstate.RefLogEntry{
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

	return mergedHistoryFixture{
		srv: srv, projectID: p.ID, rootID: rootID, fileID: fileID, nested: nested,
		commitInit: commitInit, commitA: commitA, commitB: commitB, commitC: commitC,
		transitionID: terminal[rootID],
	}
}

func (f mergedHistoryFixture) listing(t *testing.T) wire.SourceFileVersionsResponse {
	t.Helper()
	req := newAuthedRequest(http.MethodGet,
		"/v1/projects/"+f.projectID+"/source/versions?file_id="+f.fileID, nil)
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.SourceFileVersionsResponse
	testutil.FailErr(t, "decode merged listing", json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func commitsByHash(rows []wire.SourceFileCommit) map[string]wire.SourceFileCommit {
	out := make(map[string]wire.SourceFileCommit, len(rows))
	for _, row := range rows {
		out[row.Commit] = row
	}
	return out
}

// Merged history joins matching commits, arrivals, and retained versions.
func TestSourceVersionsMergedListingJoinsGitLineage(t *testing.T) {
	f := newMergedHistoryFixture(t)
	out := f.listing(t)

	if out.GitHistoryState != wire.SourceGitHistoryStateAvailable || out.TrackedAt == nil || out.NextCursor != "" {
		t.Fatalf("lane facts = available:%v tracked_at:%v next:%q",
			out.GitHistoryState, out.TrackedAt, out.NextCursor)
	}
	byHash := commitsByHash(out.Commits)
	if len(byHash) != 4 {
		t.Fatalf("commits = %+v", out.Commits)
	}
	// A shares the retained pre-image.
	if byHash[f.commitA].MatchesVersionID == nil {
		t.Fatalf("pre-image commit unmatched: %+v", byHash[f.commitA])
	}
	// C shares the observed head.
	if byHash[f.commitC].MatchesVersionID == nil {
		t.Fatalf("commit C unmatched: %+v", byHash[f.commitC])
	}
	// B belongs to the observed arrival.
	rowB := byHash[f.commitB]
	if rowB.MatchesVersionID != nil || rowB.ArrivalGitChangeID == nil ||
		*rowB.ArrivalGitChangeID != f.transitionID {
		t.Fatalf("commit B = %+v", rowB)
	}
	if len(out.Arrivals) != 1 || out.Arrivals[0].ID != f.transitionID {
		t.Fatalf("arrivals = %+v", out.Arrivals)
	}
	// Unretained commits stay in the repository-history lane.
	rowInit := byHash[f.commitInit]
	if rowInit.MatchesVersionID != nil || rowInit.ArrivalGitChangeID != nil {
		t.Fatalf("init commit = %+v", rowInit)
	}
	if rowInit.SourcePath != "packages/app/src/app.ts" {
		t.Fatalf("init commit source path = %q", rowInit.SourcePath)
	}
}

func TestSourceVersionsCanReturnOneLane(t *testing.T) {
	f := newMergedHistoryFixture(t)
	t.Run("retained", func(t *testing.T) {
		req := newAuthedRequest(http.MethodGet,
			"/v1/projects/"+f.projectID+"/source/versions?file_id="+f.fileID+"&lane=retained", nil)
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var out wire.SourceFileVersionsResponse
		testutil.FailErr(t, "decode retained listing", json.Unmarshal(w.Body.Bytes(), &out))
		if len(out.Versions) == 0 || len(out.Commits) != 0 || len(out.Arrivals) != 0 ||
			out.GitHistoryState != wire.SourceGitHistoryStateNotRequested || out.NextCursor != "" {
			t.Fatalf("retained listing = %+v", out)
		}
	})
	t.Run("git", func(t *testing.T) {
		req := newAuthedRequest(http.MethodGet,
			"/v1/projects/"+f.projectID+"/source/versions?file_id="+f.fileID+"&lane=git", nil)
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var out wire.SourceFileVersionsResponse
		testutil.FailErr(t, "decode git listing", json.Unmarshal(w.Body.Bytes(), &out))
		if len(out.Versions) != 0 || len(out.Commits) == 0 ||
			out.GitHistoryState != wire.SourceGitHistoryStateAvailable || out.NextCursor != "" {
			t.Fatalf("git listing = %+v", out)
		}
	})
}

func TestCommitStatePreviewsAndRestoresFromTheObjectStore(t *testing.T) {
	f := newMergedHistoryFixture(t)
	rowB := commitsByHash(f.listing(t).Commits)[f.commitB]
	if rowB.BlobOid == nil {
		t.Fatalf("commit B carries no blob: %+v", rowB)
	}

	compare := newAuthedRequest(http.MethodGet,
		"/v1/projects/"+f.projectID+"/source/comparison?root_id="+f.rootID+
			"&blob_oid="+*rowB.BlobOid, nil)
	cw := httptest.NewRecorder()
	f.srv.ServeHTTP(cw, compare)
	if cw.Code != http.StatusOK {
		t.Fatalf("comparison status=%d body=%s", cw.Code, cw.Body.String())
	}
	var diff wire.SourceComparison
	testutil.FailErr(t, "decode blob comparison", json.Unmarshal(cw.Body.Bytes(), &diff))
	if diff.After == nil || diff.After.Content != "intermediate\n" ||
		diff.Before == nil || diff.Before.State != "absent" {
		t.Fatalf("blob comparison = %+v", diff)
	}

	body, err := json.Marshal(wire.SourceCommitRestoreRequest{
		OperationID: uuid.NewString(), FileID: f.fileID, RootID: f.rootID,
		Path: "src/app.ts", SourcePath: rowB.SourcePath, BlobOid: *rowB.BlobOid,
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("landed\n"))},
	})
	testutil.FailErr(t, "marshal commit restore", err)
	restore := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+f.projectID+"/source/commits/"+f.commitB+"/restore",
		bytes.NewReader(body))
	rw := httptest.NewRecorder()
	f.srv.ServeHTTP(rw, restore)
	if rw.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rw.Code, rw.Body.String())
	}
	var restored wire.SourceCommitRestoreResponse
	testutil.FailErr(t, "decode commit restore", json.Unmarshal(rw.Body.Bytes(), &restored))
	if !restored.Changed || restored.Commit != f.commitB {
		t.Fatalf("restore = %+v", restored)
	}
	onDisk, err := os.ReadFile(filepath.Join(f.nested, "src", "app.ts"))
	testutil.FailErr(t, "read restored file", err)
	if string(onDisk) != "intermediate\n" {
		t.Fatalf("restored bytes = %q", onDisk)
	}
	for _, row := range f.listing(t).Commits {
		if row.Commit == f.commitB && row.MatchesVersionID == nil {
			t.Fatalf("restored commit not re-joined: %+v", row)
		}
	}
}

func TestCommitRestoreRefusesAnotherCommitsBlob(t *testing.T) {
	f := newMergedHistoryFixture(t)
	rowB := commitsByHash(f.listing(t).Commits)[f.commitB]
	if rowB.BlobOid == nil {
		t.Fatalf("commit B carries no blob: %+v", rowB)
	}
	body, err := json.Marshal(wire.SourceCommitRestoreRequest{
		OperationID: uuid.NewString(), FileID: f.fileID, RootID: f.rootID,
		Path: "src/app.ts", SourcePath: rowB.SourcePath, BlobOid: *rowB.BlobOid,
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("landed\n"))},
	})
	testutil.FailErr(t, "marshal mismatched commit restore", err)
	req := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+f.projectID+"/source/commits/"+f.commitA+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	onDisk, err := os.ReadFile(filepath.Join(f.nested, "src", "app.ts"))
	testutil.FailErr(t, "read untouched file", err)
	if string(onDisk) != "landed\n" {
		t.Fatalf("mismatched restore touched the tree: %q", onDisk)
	}
}

// Commit restore refuses drifted object identity.
func TestCommitRestoreRefusesADriftedObject(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	repo, nested := nestedRootRepo(t)
	head := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "HEAD")
	head.Env = lyexec.LocalGitEnv()
	headOut, err := head.CombinedOutput()
	testutil.FailErr(t, "git rev-parse", err)
	commit := strings.TrimSpace(string(headOut))

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "write working bytes",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), []byte("working\n"), 0o644))
	testutil.FailErr(t, "record tracked state", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser,
		Before: []byte("committed\n"), After: []byte("working\n"),
	}))
	fileID, _, err := ledger.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
	testutil.FailErr(t, "resolve file", err)

	body, err := json.Marshal(wire.SourceCommitRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID,
		Path: "src/app.ts", SourcePath: "packages/app/src/app.ts",
		BlobOid: strings.Repeat("f", 40),
		Base:    wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("working\n"))},
	})
	testutil.FailErr(t, "marshal drifted restore", err)
	req := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/source/commits/"+commit+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	onDisk, err := os.ReadFile(filepath.Join(nested, "src", "app.ts"))
	testutil.FailErr(t, "read untouched file", err)
	if string(onDisk) != "working\n" {
		t.Fatalf("drifted restore touched the tree: %q", onDisk)
	}
}

// A committed directory is absent from a file comparison.
func TestCommitBaselineTreatsADirectoryInHeadAsAbsent(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	_, nested := nestedRootRepo(t)

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	// Replace the committed directory path with a working file.
	testutil.FailErr(t, "remove committed directory",
		os.RemoveAll(filepath.Join(nested, "src")))
	testutil.FailErr(t, "write file over directory path",
		os.WriteFile(filepath.Join(nested, "src"), []byte("now a file\n"), 0o644))
	testutil.FailErr(t, "record conversion", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src",
		Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginUser,
		After: []byte("now a file\n"),
	}))

	code, comparison := getSourceComparison(t, srv, p.ID, url.Values{
		"root_id":  {rootID},
		"path":     {"src"},
		"baseline": {"commit"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if comparison.Before == nil {
		t.Fatal("commit comparison has no before endpoint")
	}
	if comparison.Before.Availability != string(sourceledger.ContentAbsent) {
		t.Fatalf("before availability = %q want absent; a tree is not this file",
			comparison.Before.Availability)
	}
	if comparison.Before.Content != "" {
		t.Fatalf("before content = %q; a tree listing must never read as file text",
			comparison.Before.Content)
	}
}

// A file that is not in the commit reads as absent.
func TestCommitBaselineReportsAnUncommittedFileAsAbsent(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withTestGitManager, withLedger)
	_, nested := nestedRootRepo(t)

	p := createProjectForTest(t, srv, nested)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "write new file",
		os.WriteFile(filepath.Join(nested, "src", "fresh.ts"), []byte("brand new\n"), 0o644))
	testutil.FailErr(t, "record create", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/fresh.ts",
		Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginUser,
		After: []byte("brand new\n"),
	}))

	code, comparison := getSourceComparison(t, srv, p.ID, url.Values{
		"root_id":  {rootID},
		"path":     {"src/fresh.ts"},
		"baseline": {"commit"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if comparison.Before == nil {
		t.Fatal("commit comparison has no before endpoint")
	}
	if comparison.Before.Availability != string(sourceledger.ContentAbsent) {
		t.Fatalf("before availability = %q want absent", comparison.Before.Availability)
	}
	if comparison.Before.Content != "" {
		t.Fatalf("before content = %q want empty", comparison.Before.Content)
	}
}

// A missing working tree makes the commit baseline unavailable.
func TestCommitBaselineWithoutAWorkingTreeReportsUnavailable(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	dir := t.TempDir()
	p := createProjectForTest(t, srv, dir)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "record edit", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "note.txt",
		Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginUser,
		After: []byte("only\n"),
	}))

	code, _ := getSourceComparison(t, srv, p.ID, url.Values{
		"root_id":  {rootID},
		"path":     {"note.txt"},
		"baseline": {"commit"},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", code)
	}
}

func TestSourceComparisonOutOfRangeOmitsBothEndpoints(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, fileID := seedRewrittenReadme(t, srv)

	code, comparison := getSourceComparison(t, srv, p.ID, url.Values{
		"file_id":  {fileID},
		"baseline": {"session:someone-else"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if comparison.InRange {
		t.Fatal("in_range = true, want false")
	}
	if comparison.Before != nil || comparison.After != nil {
		t.Fatalf("out-of-range endpoints = %+v / %+v, want both omitted",
			comparison.Before, comparison.After)
	}
}

// withoutSourceInventory leaves Git observations to the fixture: the ledger
// records history, but no inventory pass observes the workspace.
func withoutSourceInventory(d *Dependencies) { d.SourceInventory = nil }
