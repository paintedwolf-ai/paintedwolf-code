package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitstate"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCommitBaselineReadsTheNestedRootsOwnFile(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	_, nested := contractfixture.NestedRootRepo(t)

	p := contractfixture.CreateProjectForTest(t, srv, nested)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "edit working file",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), []byte("working\n"), 0o644))
	testutil.FailErr(t, "record edit", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts",
		Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginUser,
		Before: []byte("committed\n"), After: []byte("working\n"),
	}))

	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
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

func TestRestoreGitCausedVersionUsesGitObjectStore(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger, contractfixture.WithPassiveSourceInventory, contractfixture.WithSessionStore(sessionstore.NewSQL(ledgerDB)))
	repo, nested := contractfixture.NestedRootRepo(t)

	p := contractfixture.CreateProjectForTest(t, srv, nested)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
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
	reader := contractfixture.NewScriptedGitStateReader(t, nested,
		gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: first, HeadRef: "main"})
	ledger.Git.SetGitReader(reader)
	_, err := ledger.Git.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "seed git state", err)

	gitBytes := []byte("from git\n")
	testutil.FailErr(t, "write committed bytes",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), gitBytes, 0o644))
	commitAll("rewrite from git")
	second := revParse()
	reader.Put(nested, gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: second, HeadRef: "main",
	})
	reader.PutLog(nested, []gitstate.RefLogEntry{
		{Commit: second, Subject: "commit: rewrite from git"},
		{Commit: first, Subject: "commit: init"},
	})
	terminal, err := ledger.Git.ObserveGitState(t.Context(), p.ID,
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
	fileID, gitVersionID, err := ledger.History.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
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
	req := contractfixture.NewAuthedRequest(http.MethodPost,
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
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger, contractfixture.WithPassiveSourceInventory)
	_, nested := contractfixture.NestedRootRepo(t)

	p := contractfixture.CreateProjectForTest(t, srv, nested)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	reader := contractfixture.NewScriptedGitStateReader(t, nested,
		gitstate.State{Repo: gitstate.RepoPresent, HeadCommit: "0000", HeadRef: "main"})
	ledger.Git.SetGitReader(reader)
	_, err := ledger.Git.ObserveGitState(t.Context(), p.ID,
		[]sourceledger.RootSpec{{ID: rootID, Path: nested}})
	testutil.FailErr(t, "seed git state", err)
	reader.Put(nested, gitstate.State{
		Repo: gitstate.RepoPresent, HeadCommit: "1111", HeadRef: "main",
	})
	reader.PutLog(nested, []gitstate.RefLogEntry{
		{Commit: "1111", Subject: "commit: claimed"},
		{Commit: "0000", Subject: "commit: earlier"},
	})
	terminal, err := ledger.Git.ObserveGitState(t.Context(), p.ID,
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
	fileID, versionID, err := ledger.History.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
	testutil.FailErr(t, "resolve drifted version", err)

	body, err := json.Marshal(wire.SourceVersionRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID, Path: "src/app.ts",
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("committed\n"))},
	})
	testutil.FailErr(t, "marshal restore request", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/source/versions/"+versionID+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// mergedHistoryFixture spans commit-only and retained history.

func TestSourceVersionsMergedListingJoinsGitLineage(t *testing.T) {
	f := contractfixture.NewMergedHistoryFixture(t)
	out := f.Listing(t)

	if out.GitHistoryState != wire.SourceGitHistoryStateAvailable || out.TrackedAt == nil || out.NextCursor != "" {
		t.Fatalf("lane facts = available:%v tracked_at:%v next:%q",
			out.GitHistoryState, out.TrackedAt, out.NextCursor)
	}
	byHash := contractfixture.CommitsByHash(out.Commits)
	if len(byHash) != 4 {
		t.Fatalf("commits = %+v", out.Commits)
	}
	// A shares the retained pre-image.
	if byHash[f.CommitA].MatchesVersionID == nil {
		t.Fatalf("pre-image commit unmatched: %+v", byHash[f.CommitA])
	}
	// C shares the observed head.
	if byHash[f.CommitC].MatchesVersionID == nil {
		t.Fatalf("commit C unmatched: %+v", byHash[f.CommitC])
	}
	// B belongs to the observed arrival.
	rowB := byHash[f.CommitB]
	if rowB.MatchesVersionID != nil || rowB.ArrivalGitChangeID == nil ||
		*rowB.ArrivalGitChangeID != f.TransitionID {
		t.Fatalf("commit B = %+v", rowB)
	}
	if len(out.Arrivals) != 1 || out.Arrivals[0].ID != f.TransitionID {
		t.Fatalf("arrivals = %+v", out.Arrivals)
	}
	// Unretained commits stay in the repository-history lane.
	rowInit := byHash[f.CommitInit]
	if rowInit.MatchesVersionID != nil || rowInit.ArrivalGitChangeID != nil {
		t.Fatalf("init commit = %+v", rowInit)
	}
	if rowInit.SourcePath != "packages/app/src/app.ts" {
		t.Fatalf("init commit source path = %q", rowInit.SourcePath)
	}
}

func TestSourceVersionsCanReturnOneLane(t *testing.T) {
	f := contractfixture.NewMergedHistoryFixture(t)
	t.Run("retained", func(t *testing.T) {
		req := contractfixture.NewAuthedRequest(http.MethodGet,
			"/v1/projects/"+f.ProjectID+"/source/versions?file_id="+f.FileID+"&lane=retained", nil)
		w := httptest.NewRecorder()
		f.Srv.ServeHTTP(w, req)
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
		req := contractfixture.NewAuthedRequest(http.MethodGet,
			"/v1/projects/"+f.ProjectID+"/source/versions?file_id="+f.FileID+"&lane=git", nil)
		w := httptest.NewRecorder()
		f.Srv.ServeHTTP(w, req)
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
	f := contractfixture.NewMergedHistoryFixture(t)
	rowB := contractfixture.CommitsByHash(f.Listing(t).Commits)[f.CommitB]
	if rowB.BlobOid == nil {
		t.Fatalf("commit B carries no blob: %+v", rowB)
	}

	compare := contractfixture.NewAuthedRequest(http.MethodGet,
		"/v1/projects/"+f.ProjectID+"/source/comparison?root_id="+f.RootID+
			"&blob_oid="+*rowB.BlobOid, nil)
	cw := httptest.NewRecorder()
	f.Srv.ServeHTTP(cw, compare)
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
		OperationID: uuid.NewString(), FileID: f.FileID, RootID: f.RootID,
		Path: "src/app.ts", SourcePath: rowB.SourcePath, BlobOid: *rowB.BlobOid,
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("landed\n"))},
	})
	testutil.FailErr(t, "marshal commit restore", err)
	restore := contractfixture.NewAuthedRequest(http.MethodPost,
		"/v1/projects/"+f.ProjectID+"/source/commits/"+f.CommitB+"/restore",
		bytes.NewReader(body))
	rw := httptest.NewRecorder()
	f.Srv.ServeHTTP(rw, restore)
	if rw.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rw.Code, rw.Body.String())
	}
	var restored wire.SourceCommitRestoreResponse
	testutil.FailErr(t, "decode commit restore", json.Unmarshal(rw.Body.Bytes(), &restored))
	if !restored.Changed || restored.Commit != f.CommitB {
		t.Fatalf("restore = %+v", restored)
	}
	onDisk, err := os.ReadFile(filepath.Join(f.Nested, "src", "app.ts"))
	testutil.FailErr(t, "read restored file", err)
	if string(onDisk) != "intermediate\n" {
		t.Fatalf("restored bytes = %q", onDisk)
	}
	for _, row := range f.Listing(t).Commits {
		if row.Commit == f.CommitB && row.MatchesVersionID == nil {
			t.Fatalf("restored commit not re-joined: %+v", row)
		}
	}
}

func TestCommitRestoreRefusesAnotherCommitsBlob(t *testing.T) {
	f := contractfixture.NewMergedHistoryFixture(t)
	rowB := contractfixture.CommitsByHash(f.Listing(t).Commits)[f.CommitB]
	if rowB.BlobOid == nil {
		t.Fatalf("commit B carries no blob: %+v", rowB)
	}
	body, err := json.Marshal(wire.SourceCommitRestoreRequest{
		OperationID: uuid.NewString(), FileID: f.FileID, RootID: f.RootID,
		Path: "src/app.ts", SourcePath: rowB.SourcePath, BlobOid: *rowB.BlobOid,
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("landed\n"))},
	})
	testutil.FailErr(t, "marshal mismatched commit restore", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost,
		"/v1/projects/"+f.ProjectID+"/source/commits/"+f.CommitA+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	onDisk, err := os.ReadFile(filepath.Join(f.Nested, "src", "app.ts"))
	testutil.FailErr(t, "read untouched file", err)
	if string(onDisk) != "landed\n" {
		t.Fatalf("mismatched restore touched the tree: %q", onDisk)
	}
}

// Commit restore refuses drifted object identity.

func TestCommitRestoreRefusesADriftedObject(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	repo, nested := contractfixture.NestedRootRepo(t)
	head := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "HEAD")
	head.Env = lyexec.LocalGitEnv()
	headOut, err := head.CombinedOutput()
	testutil.FailErr(t, "git rev-parse", err)
	commit := strings.TrimSpace(string(headOut))

	p := contractfixture.CreateProjectForTest(t, srv, nested)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "write working bytes",
		os.WriteFile(filepath.Join(nested, "src", "app.ts"), []byte("working\n"), 0o644))
	testutil.FailErr(t, "record tracked state", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser,
		Before: []byte("committed\n"), After: []byte("working\n"),
	}))
	fileID, _, err := ledger.History.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "src/app.ts")
	testutil.FailErr(t, "resolve file", err)

	body, err := json.Marshal(wire.SourceCommitRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID,
		Path: "src/app.ts", SourcePath: "packages/app/src/app.ts",
		BlobOid: strings.Repeat("f", 40),
		Base:    wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256([]byte("working\n"))},
	})
	testutil.FailErr(t, "marshal drifted restore", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost,
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
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	_, nested := contractfixture.NestedRootRepo(t)

	p := contractfixture.CreateProjectForTest(t, srv, nested)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
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

	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
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
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, contractfixture.WithTestGitManager, withLedger)
	_, nested := contractfixture.NestedRootRepo(t)

	p := contractfixture.CreateProjectForTest(t, srv, nested)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "write new file",
		os.WriteFile(filepath.Join(nested, "src", "fresh.ts"), []byte("brand new\n"), 0o644))
	testutil.FailErr(t, "record create", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "src/fresh.ts",
		Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginUser,
		After: []byte("brand new\n"),
	}))

	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
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
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	dir := t.TempDir()
	p := contractfixture.CreateProjectForTest(t, srv, dir)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "record edit", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "note.txt",
		Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginUser,
		After: []byte("only\n"),
	}))

	code, _ := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
		"root_id":  {rootID},
		"path":     {"note.txt"},
		"baseline": {"commit"},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", code)
	}
}

func TestSourceComparisonOutOfRangeOmitsBothEndpoints(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)

	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
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
