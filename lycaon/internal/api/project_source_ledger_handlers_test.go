package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// testSourceLedger opens a ledger database and returns the option that serves
// it. Open it before the server so the server drains before the database closes.
func testSourceLedger(t *testing.T) (*sourceledger.Store, *db.Store, testDeps) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	st := sourceledger.New(sqlDB, t.TempDir())
	return st, sqlDB, func(d *Dependencies) {
		d.SourceLedger, d.SourceInventory = st, st
		d.SourceMutations = project.NewSourceMutationService(sqlDB, st)
	}
}

// withSessionStore serves sessions from store.
func withSessionStore(store session.Store) testDeps {
	return func(d *Dependencies) { d.Store = store }
}

// withWorkers serves worker jobs from queue.
func withWorkers(queue worker.WorkerQueue) testDeps {
	return func(d *Dependencies) { d.Workers = queue }
}

func TestRestoreProjectSourceVersionMakesSelectedBytesCurrent(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger, withSessionStore(sessionstore.NewSQL(ledgerDB)))
	rootPath := t.TempDir()
	p := createProjectForTest(t, srv, rootPath)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	oldContent, currentContent := []byte("old\n"), []byte("current\n")
	testutil.FailErr(t, "write current source", os.WriteFile(filepath.Join(rootPath, "note.txt"), currentContent, 0o640))
	testutil.FailErr(t, "record source history", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "note.txt", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginAgent, SessionID: "session-before", Turn: 1,
		Before: oldContent, After: currentContent,
	}))
	walk, err := ledger.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "session-before"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query source history", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("walk = %+v", walk.Files)
	}
	fileID := walk.Files[0].FileID
	versionID := walk.Files[0].Effects[0].BeforeVersionID
	body, err := json.Marshal(wire.SourceVersionRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID, Path: "note.txt",
		Base: wire.SourceTip{State: wire.SourceTipStateContent, Sha256: textfile.SHA256(currentContent)},
	})
	testutil.FailErr(t, "marshal restore request", err)
	req := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/source/versions/"+versionID+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response wire.SourceVersionRestoreResponse
	testutil.FailErr(t, "decode restore response", json.Unmarshal(w.Body.Bytes(), &response))
	if !response.Changed || response.PreviousVersionID == "" || response.Sha256 != textfile.SHA256(oldContent) {
		t.Fatalf("restore response = %+v", response)
	}
	restored, err := os.ReadFile(filepath.Join(rootPath, "note.txt"))
	testutil.FailErr(t, "read restored source", err)
	if string(restored) != string(oldContent) {
		t.Fatalf("restored source = %q", restored)
	}
	history, err := ledger.QueryFileVersions(t.Context(), p.ID, fileID, 10, 0)
	testutil.FailErr(t, "query restored version", err)
	if len(history.Versions) == 0 || history.Versions[0].DerivedFromVersionID != versionID ||
		history.Versions[0].Cause != sourceledger.CauseVersionRestore {
		t.Fatalf("restored history = %+v", history.Versions)
	}
}

func TestRestoreProjectSourceVersionRejectsUnavailableBytesWithoutMutation(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	rootPath := t.TempDir()
	p := createProjectForTest(t, srv, rootPath)
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "record uncaptured source", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    rootID, Path: "missing.bin", Op: wire.SourceChangeOpCreate,
		Origin: wire.SourceChangeOriginExternal, AfterSHA256: strings.Repeat("a", 64), AfterSize: 12,
	}))
	fileID, versionID, err := ledger.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "missing.bin")
	testutil.FailErr(t, "resolve uncaptured source", err)
	body, err := json.Marshal(wire.SourceVersionRestoreRequest{
		OperationID: uuid.NewString(), FileID: fileID, RootID: rootID, Path: "missing.bin",
		Base: wire.SourceTip{State: wire.SourceTipStateAbsent},
	})
	testutil.FailErr(t, "marshal unavailable restore", err)
	req := newAuthedRequest(http.MethodPost,
		"/v1/projects/"+p.ID+"/source/versions/"+versionID+"/restore",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(rootPath, "missing.bin")); !os.IsNotExist(err) {
		t.Fatalf("unavailable restore touched working tree: %v", err)
	}
}

func mirrorLedgerProject(t *testing.T, sqlDB db.Handle, p wire.Project) {
	t.Helper()
	testdbseed.InsertProject(t, sqlDB, p.ID)
	for _, root := range p.Roots {
		isPrimary := 0
		if root.IsPrimary {
			isPrimary = 1
		}
		_, err := sqlDB.ExecContext(t.Context(), `
			INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, root.ID, p.ID, root.Path, root.Label, isPrimary, db.FormatTime(time.Now().UTC()))
		testutil.FailErr(t, "mirror ledger project root", err)
	}
}

func prepareLedgerInventory(t *testing.T, srv *Server, ledger *sourceledger.Store, p wire.Project) {
	t.Helper()
	drainBackground(t, srv)
	roots := make([]sourceledger.RootSpec, 0, len(p.Roots))
	for _, root := range p.Roots {
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, Path: root.Path})
	}
	testutil.FailErr(t, "prepare source inventory", ledger.EnsureInventory(t.Context(), sourceledger.InventoryRequest{
		ProjectID: p.ID, RootsGeneration: p.RootsGeneration, Roots: roots,
	}))
}

func TestProjectSourceVersionsListsCompleteFileHistory(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	for _, revision := range []struct {
		op     wire.SourceChangeOp
		origin wire.SourceChangeOrigin
		after  string
	}{
		{op: wire.SourceChangeOpCreate, origin: wire.SourceChangeOriginAgent, after: "one\n"},
		{op: wire.SourceChangeOpWrite, origin: wire.SourceChangeOriginUser, after: "two\n"},
	} {
		testutil.FailErr(t, "record primary revision", ledger.Record(t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID, RootID: rootID, Path: "a.go",
			Op: revision.op, Origin: revision.origin, After: []byte(revision.after),
		}))
	}
	fileID, _, err := ledger.ResolveFile(t.Context(), p.ID, sourcebranch.Trunk, rootID, "a.go")
	testutil.FailErr(t, "resolve file", err)
	testutil.FailErr(t, "record worker revision", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, BranchID: "worker-1", RootID: rootID, Path: "a.go", FileID: fileID, JobID: "worker-1",
		Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent, After: []byte("overlay\n"),
	}))

	requestPage := func(cursor string) wire.SourceFileVersionsResponse {
		t.Helper()
		target := "/v1/projects/" + p.ID + "/source/versions?file_id=" +
			url.QueryEscape(fileID) + "&limit=1"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		req := newAuthedRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var out wire.SourceFileVersionsResponse
		testutil.FailErr(t, "decode versions", json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}

	first := requestPage("")
	if first.FileID != fileID || len(first.Versions) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	// A cursor continues only the lanes of the listing that issued it.
	otherLane := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/versions?file_id="+
		url.QueryEscape(fileID)+"&lane=retained&cursor="+url.QueryEscape(first.NextCursor), nil)
	otherLaneW := httptest.NewRecorder()
	srv.ServeHTTP(otherLaneW, otherLane)
	if otherLaneW.Code != http.StatusBadRequest {
		t.Fatalf("cursor from lane all on lane retained: status=%d body=%s", otherLaneW.Code, otherLaneW.Body.String())
	}
	if first.Current.State != wire.SourceTipStateContent || first.Current.Sha256 != textfile.SHA256([]byte("two\n")) {
		t.Fatalf("current tip = %+v", first.Current)
	}
	if first.Versions[0].WorkspaceKind != wire.SourceWorkspaceKindWorker || first.Versions[0].WorkerID == nil {
		t.Fatalf("newest version = %+v, want worker branch", first.Versions[0])
	}
	// Newer inserts do not shift the seek window.
	testutil.FailErr(t, "record concurrent primary revision", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, RootID: rootID, Path: "a.go",
		FileID: fileID, Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent,
		After: []byte("three\n"),
	}))

	// Verify descending, unique, complete pagination.
	seen := map[string]bool{first.Versions[0].ID: true}
	cursor := first.NextCursor
	origins := map[wire.SourceChangeOrigin]int{}
	for pages := 0; cursor != ""; pages++ {
		if pages > 16 {
			t.Fatal("version paging did not terminate")
		}
		page := requestPage(cursor)
		if len(page.Versions) != 1 {
			t.Fatalf("page at %s = %+v, want one version", cursor, page)
		}
		version := page.Versions[0]
		if seen[version.ID] {
			t.Fatalf("version %s returned twice", version.ID)
		}
		seen[version.ID] = true
		if version.Origin != nil {
			origins[*version.Origin]++
		}
		if page.NextCursor == cursor {
			t.Fatalf("cursor did not advance: %s", cursor)
		}
		cursor = page.NextCursor
	}
	if len(seen) < 3 {
		t.Fatalf("paged %d versions, want at least the create, the edit, and the worker write", len(seen))
	}
	if origins[wire.SourceChangeOriginUser] == 0 || origins[wire.SourceChangeOriginAgent] == 0 {
		t.Fatalf("paged origins = %+v, want both the user edit and the agent writes", origins)
	}
}

func TestProjectSourceListsRejectInvalidPageQuery(t *testing.T) {
	_, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	tests := map[string]string{
		"walk malformed limit":      "/source/walk?limit=many",
		"walk zero limit":           "/source/walk?limit=0",
		"walk large limit":          "/source/walk?limit=501",
		"walk plaintext cursor":     "/source/walk?cursor=12",
		"walk outside malformed":    "/source/walk?baseline=session:s1&include_outside_changes=yes",
		"walk outside off session":  "/source/walk?include_outside_changes=true",
		"versions malformed cursor": "/source/versions?file_id=file-1&cursor=later",
		"versions invalid lane":     "/source/versions?file_id=file-1&lane=sometimes",
	}
	for name, suffix := range tests {
		t.Run(name, func(t *testing.T) {
			req := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+suffix, nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMarkUserEditsRejectsQueriesItCannotAnswer(t *testing.T) {
	_, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	tests := map[string]string{
		"walk malformed":          "/source/walk?mark_user_edits=yes",
		"walk commit baseline":    "/source/walk?baseline=commit&mark_user_edits=false",
		"comparison malformed":    "/source/comparison?file_id=file-1&mark_user_edits=yes",
		"comparison effect mode":  "/source/comparison?effect_id=effect-1&mark_user_edits=false",
		"comparison version mode": "/source/comparison?version_id=version-1&mark_user_edits=true",
		"comparison commit path":  "/source/comparison?root_id=r1&path=a.txt&baseline=commit&mark_user_edits=false",
	}
	for name, suffix := range tests {
		t.Run(name, func(t *testing.T) {
			req := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+suffix, nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// Worker overlays stay root-addressed when paths collide.
func TestWorkerChangesReturnsLedgerRowsByRoot(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	testdbseed.InsertProject(t, ledgerDB, "p1")
	_, err := ledgerDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at)
		VALUES ('r1', 'p1', '/tmp/api-ledger-root', 'api-ledger-root', 1, ?)
	`, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert ledger root", err)

	q := worker.NewInMemoryQueue(1)
	parent := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	jobID, err := q.EnqueueWithProjectID(t.Context(), "p1", wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ProjectID:       "p1",
		ParentSessionID: parent,
	})
	testutil.FailErr(t, "enqueue", err)
	srv := newTestServer(t, withLedger, withWorkers(q))

	after := []byte("branch\n")
	err = ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: "p1", BranchID: "worker-branch", RootID: "r1", Path: "a.go",
		Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent,
		JobID: jobID, After: after,
	})
	testutil.FailErr(t, "record worker change", err)

	req := newAuthedRequest(http.MethodGet, "/v1/workers/"+jobID+"/changes", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.WorkerJobChangesResponse
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &out))
	if len(out.Files) != 1 {
		t.Fatalf("files=%+v, want one row", out.Files)
	}
	got := out.Files[0]
	if got.RootID != "r1" || got.Path != "a.go" || got.Op != wire.SourceChangeOpWrite {
		t.Fatalf("files[0]=%+v, want r1/a.go write", got)
	}
}

func TestCreateProjectSourcePinRejectsMalformedJSONWithoutMutation(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	prepareLedgerInventory(t, srv, ledger, p)

	req := newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/pins", strings.NewReader(`{"label":`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	page, err := ledger.ListPinsPage(t.Context(), p.ID, sourceledger.PinPageQuery{})
	testutil.FailErr(t, "list pins", err)
	if len(page.Pins) != 0 {
		t.Fatalf("pins = %+v, want no mutation", page.Pins)
	}

	optional := newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/pins", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, optional)
	if w.Code != http.StatusCreated {
		t.Fatalf("optional body status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestSourcePinsPageWithoutExpiringHistory(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	prepareLedgerInventory(t, srv, ledger, p)
	for i := 0; i < 3; i++ {
		_, err := ledger.CreatePin(t.Context(), p.ID, "pin "+strconv.Itoa(i))
		testutil.FailErr(t, "create pin", err)
	}

	req := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/pins?limit=2", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first page status = %d body = %s", w.Code, w.Body.String())
	}
	var first wire.SourcePinList
	testutil.FailErr(t, "decode first page", json.Unmarshal(w.Body.Bytes(), &first))
	if len(first.Pins) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	req = newAuthedRequest(http.MethodGet,
		"/v1/projects/"+p.ID+"/source/pins?limit=2&cursor="+url.QueryEscape(first.NextCursor), nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second page status = %d body = %s", w.Code, w.Body.String())
	}
	var second wire.SourcePinList
	testutil.FailErr(t, "decode second page", json.Unmarshal(w.Body.Bytes(), &second))
	if len(second.Pins) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}

	req = newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/pins?cursor=invalid", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestSourcePinsExcludeStructuralCheckpoints(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	prepareLedgerInventory(t, srv, ledger, p)

	req := newAuthedRequest(
		http.MethodPost,
		"/v1/projects/"+p.ID+"/source/pins",
		strings.NewReader(`{"label":"Before refactor"}`),
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var pin wire.SourcePin
	testutil.FailErr(t, "decode pin", json.Unmarshal(w.Body.Bytes(), &pin))

	page, err := ledger.ListPinsPage(t.Context(), p.ID, sourceledger.PinPageQuery{})
	testutil.FailErr(t, "list pins", err)
	if len(page.Pins) != 1 || page.Pins[0].ID != pin.ID {
		t.Fatalf("pins = %+v, want pin %q", page.Pins, pin.ID)
	}
	structural := make([]sourceledger.Checkpoint, 0, 2)
	for _, input := range []sourceledger.StructuralCheckpointInput{
		{ProjectID: p.ID, Kind: sourceledger.CheckpointSession, SessionID: "session-1"},
		{ProjectID: p.ID, Kind: sourceledger.CheckpointTurn, SessionID: "session-1", Turn: 1},
	} {
		checkpoint, createErr := ledger.CreateStructuralCheckpoint(t.Context(), input)
		testutil.FailErr(t, "create structural checkpoint", createErr)
		structural = append(structural, checkpoint)
	}

	req = newAuthedRequest(
		http.MethodPatch,
		"/v1/projects/"+p.ID+"/source/pins/"+pin.ID,
		strings.NewReader(`{"label":"Renamed"}`),
	)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", w.Code, w.Body.String())
	}

	req = newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/pins", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list pins status = %d body = %s", w.Code, w.Body.String())
	}
	var pinList wire.SourcePinList
	testutil.FailErr(t, "decode pins", json.Unmarshal(w.Body.Bytes(), &pinList))
	if len(pinList.Pins) != 1 || pinList.Pins[0].ID != pin.ID || pinList.Pins[0].Label != "Renamed" {
		t.Fatalf("pins = %+v, want renamed pin %q", pinList.Pins, pin.ID)
	}
	for _, checkpoint := range structural {
		for _, mutation := range []struct {
			method string
			body   io.Reader
		}{
			{method: http.MethodPatch, body: strings.NewReader(`{"label":"Hidden"}`)},
			{method: http.MethodDelete},
		} {
			req = newAuthedRequest(
				mutation.method,
				"/v1/projects/"+p.ID+"/source/pins/"+checkpoint.ID,
				mutation.body,
			)
			w = httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("structural %s %s status = %d body = %s", checkpoint.Kind, mutation.method, w.Code, w.Body.String())
			}
		}
		req = newAuthedRequest(
			http.MethodGet,
			"/v1/projects/"+p.ID+"/source/walk?baseline="+url.QueryEscape("pin:"+checkpoint.ID),
			nil,
		)
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("structural baseline %s status = %d body = %s", checkpoint.Kind, w.Code, w.Body.String())
		}
	}

	req = newAuthedRequest(http.MethodDelete, "/v1/projects/"+p.ID+"/source/pins/"+pin.ID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestProjectSourcePresentationStopsAtDisplayedRevision(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	for _, after := range []string{"first\n", "second\n"} {
		testutil.FailErr(t, "record agent revision", ledger.Record(t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID, RootID: rootID, Path: "a.go",
			Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent,
			After: []byte(after),
		}))
	}
	var effectID, fileID string
	var ordinal int64
	testutil.FailErr(t, "read first revision", ledgerDB.QueryRowContext(t.Context(), `
		SELECT id, file_id, ordinal FROM source_effects
		WHERE project_id = ? AND root_id = ? AND path = ?
		ORDER BY ordinal ASC LIMIT 1
	`, p.ID, rootID, "a.go").Scan(&effectID, &fileID, &ordinal))
	body, err := json.Marshal(wire.SourcePresentationCompletion{
		FileID: fileID, EffectID: effectID, Ordinal: ordinal,
	})
	testutil.FailErr(t, "encode presentation", err)
	req := newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/seen", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var unpresented int
	testutil.FailErr(t, "count unpresented revisions", ledgerDB.QueryRowContext(t.Context(), `
		SELECT count(*) FROM source_agent_presentations
		WHERE project_id = ? AND file_id = ?
	`, p.ID, fileID).Scan(&unpresented))
	if unpresented != 1 {
		t.Fatalf("unpresented revisions = %d want 1", unpresented)
	}
}

func TestWorkerChangesRejectsUnknownWorker(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)

	q := worker.NewInMemoryQueue(1)
	parent := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	_, err := q.EnqueueWithProjectID(t.Context(), "p1", wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ProjectID:       "p1",
		ParentSessionID: parent,
	})
	testutil.FailErr(t, "enqueue", err)
	srv := newTestServer(t, withLedger, withWorkers(q))

	for _, tc := range []struct{ name, path string }{
		{"unknown job", "/v1/workers/unknown-job/changes"},
	} {
		req := newAuthedRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d body=%s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestWorkerChangesWithoutRecordedEditsIsEmpty(t *testing.T) {
	q := worker.NewInMemoryQueue(1)
	parent := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	jobID, err := q.EnqueueWithProjectID(t.Context(), "p1", wire.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ProjectID:       "p1",
		ParentSessionID: parent,
	})
	testutil.FailErr(t, "enqueue", err)
	srv := newTestServer(t, withWorkers(q))

	req := newAuthedRequest(http.MethodGet, "/v1/workers/"+jobID+"/changes", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.WorkerJobChangesResponse
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &out))
	if len(out.Files) != 0 {
		t.Fatalf("files=%+v, want empty", out.Files)
	}
}

func TestMapSourceWalkCarriesRenameOrigin(t *testing.T) {
	res := sourceledger.WalkResult{Files: []sourceledger.WalkFile{{
		FileID: "file-1",
		RootID: "root-1",
		Path:   "new/name.go",
		Effects: []sourceledger.Effect{{
			ID:             "change-1",
			ProjectID:      "project-1",
			FileID:         "file-1",
			AfterVersionID: "version-1",
			RootID:         "root-1",
			Path:           "new/name.go",
			FromPath:       "old/name.go",
			Op:             wire.SourceChangeOpRename,
			Origin:         wire.SourceChangeOriginAgent,
			TS:             time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		}},
	}}}

	got := sourceapi.MapSourceWalk(res)
	if len(got.Files) != 1 || len(got.Files[0].Effects) != 1 {
		t.Fatalf("mapped changes = %+v", got.Files)
	}
	fromPath := got.Files[0].Effects[0].FromPath
	if fromPath == nil || *fromPath != "old/name.go" {
		t.Fatalf("from_path = %v, want old/name.go", fromPath)
	}
}

func TestMapSourceWalkListsGitChangesAndGatesReferences(t *testing.T) {
	res := sourceledger.WalkResult{
		GitChanges: []sourceledger.GitTransition{{
			ID: "transition-1", ProjectID: "project-1", RootID: "root-1",
			Kind: string(wire.SourceGitChangeCommit), FromCommit: "aaa", ToCommit: "bbb",
			Detail: "Save the draft", Ordinal: 7,
			SessionID: "session-1", Turn: 2, ToolCallID: "commit-call", ToolName: "git_commit",
			ObservedTS: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		}},
		Files: []sourceledger.WalkFile{{
			FileID: "file-1", RootID: "root-1", Path: "main.go",
			Effects: []sourceledger.Effect{
				{
					ID: "change-1", ProjectID: "project-1", FileID: "file-1",
					AfterVersionID: "version-1", BranchID: "worker-1",
					RootID: "root-1", Path: "main.go", Op: wire.SourceChangeOpWrite,
					Origin:          wire.SourceChangeOriginExternal,
					GitTransitionID: "transition-1",
				},
				{
					ID: "change-2", ProjectID: "project-1", FileID: "file-1",
					AfterVersionID: "version-2", BranchID: "worker-1",
					RootID: "root-1", Path: "main.go", Op: wire.SourceChangeOpWrite,
					Origin:          wire.SourceChangeOriginExternal,
					GitTransitionID: "transition-unresolved",
				},
			},
		}},
	}

	got := sourceapi.MapSourceWalk(res)
	if len(got.GitChanges) != 1 || got.GitChanges[0].ID != "transition-1" ||
		got.GitChanges[0].Kind != wire.SourceGitChangeCommit ||
		got.GitChanges[0].SessionID != "session-1" || got.GitChanges[0].Turn != 2 ||
		got.GitChanges[0].ToolCallID != "commit-call" || got.GitChanges[0].ToolName != "git_commit" {
		t.Fatalf("git_changes = %+v", got.GitChanges)
	}
	effects := got.Files[0].Effects
	if effects[0].GitChangeID == nil || *effects[0].GitChangeID != "transition-1" {
		t.Fatalf("resolved reference = %v, want transition-1", effects[0].GitChangeID)
	}
	// Omit unresolved references.
	if effects[1].GitChangeID != nil {
		t.Fatalf("unresolved reference = %v, want omitted", *effects[1].GitChangeID)
	}
}

func TestMapSourceVersionEmbedsGitChange(t *testing.T) {
	transitions := map[string]sourceledger.GitTransition{"transition-1": {
		ID: "transition-1", ProjectID: "project-1", RootID: "root-1",
		Kind: string(wire.SourceGitChangeCheckout), FromRef: "main", ToRef: "work",
		Ordinal: 3, ObservedTS: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	}}
	caused := sourceapi.MapSourceVersion(sourceledger.Version{
		ID: "version-1", FileID: "file-1", BranchID: "worker-1",
		RootID: "root-1",
		Path:   "main.go", State: "content", GitTransitionID: "transition-1",
	}, transitions, nil)
	if caused.GitChange == nil || caused.GitChange.ID != "transition-1" ||
		caused.GitChange.Kind != wire.SourceGitChangeCheckout {
		t.Fatalf("git_change = %+v", caused.GitChange)
	}
	plain := sourceapi.MapSourceVersion(sourceledger.Version{
		ID: "version-2", FileID: "file-1", BranchID: "worker-1",
		RootID: "root-1",
		Path:   "main.go", State: "content",
	}, transitions, nil)
	if plain.GitChange != nil {
		t.Fatalf("git_change = %+v, want omitted", plain.GitChange)
	}
	windows := map[string]sourceledger.CommandWindow{"window-1": {
		ID: "window-1", ProjectID: "project-1", SessionID: "session-1", Turn: 2,
		ToolCallID: "call-1", ToolName: "command", CommandLine: "cargo build",
		State: wire.SourceCommandWindowEnded, AdmissionMode: "host_floor", Ordinal: 5,
		StartedTS: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		EndedTS:   time.Date(2026, 8, 9, 12, 0, 30, 0, time.UTC),
	}}
	observed := sourceapi.MapSourceVersion(sourceledger.Version{
		ID: "version-3", FileID: "file-1", BranchID: "worker-1",
		RootID: "root-1",
		Path:   "Cargo.lock", State: "content", CommandWindowID: "window-1",
	}, transitions, windows)
	if observed.Command == nil || observed.Command.ID != "window-1" ||
		observed.Command.CommandLine != "cargo build" || observed.Command.EndedAt == nil ||
		observed.Command.AdmissionMode == nil || *observed.Command.AdmissionMode != "host_floor" {
		t.Fatalf("command = %+v", observed.Command)
	}
}

func TestMapSourceWalkListsCommandsAndGatesReferences(t *testing.T) {
	res := sourceledger.WalkResult{
		Commands: []sourceledger.CommandWindow{{
			ID: "window-1", ProjectID: "project-1", SessionID: "session-1", Turn: 1,
			ToolName: "command", CommandLine: "cargo build", State: wire.SourceCommandWindowRunning,
			Ordinal: 4, StartedTS: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		}},
		Files: []sourceledger.WalkFile{{
			FileID: "file-1", RootID: "root-1", Path: "Cargo.lock",
			Effects: []sourceledger.Effect{
				{
					ID: "change-1", ProjectID: "project-1", FileID: "file-1",
					AfterVersionID: "version-1", BranchID: "worker-1",
					RootID: "root-1", Path: "Cargo.lock", Op: wire.SourceChangeOpCreate,
					Origin:          wire.SourceChangeOriginExternal,
					CommandWindowID: "window-1",
				},
				{
					ID: "change-2", ProjectID: "project-1", FileID: "file-1",
					AfterVersionID: "version-2", BranchID: "worker-1",
					RootID: "root-1", Path: "Cargo.lock", Op: wire.SourceChangeOpWrite,
					Origin:          wire.SourceChangeOriginExternal,
					CommandWindowID: "window-unresolved",
				},
			},
		}},
	}

	got := sourceapi.MapSourceWalk(res)
	if len(got.Commands) != 1 || got.Commands[0].ID != "window-1" ||
		got.Commands[0].State != wire.SourceCommandWindowRunning || got.Commands[0].EndedAt != nil ||
		got.Commands[0].AdmissionMode != nil {
		t.Fatalf("commands = %+v", got.Commands)
	}
	effects := got.Files[0].Effects
	if effects[0].CommandID == nil || *effects[0].CommandID != "window-1" {
		t.Fatalf("resolved reference = %v, want window-1", effects[0].CommandID)
	}
	if effects[1].CommandID != nil {
		t.Fatalf("unresolved reference = %v, want omitted", *effects[1].CommandID)
	}
}

func TestMapSourceEffectCarriesToolIdentity(t *testing.T) {
	got := sourceapi.MapSourceEffect(sourceledger.Effect{
		ID: "change-1", ProjectID: "project-1", FileID: "file-1",
		AfterVersionID: "version-1", BranchID: "worker-1",
		RootID: "root-1", Path: "main.go", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginAgent, ToolCallID: "call-1", ToolName: "edit",
	})
	if got.ToolCallID == nil || *got.ToolCallID != "call-1" {
		t.Fatalf("tool_call_id = %v, want call-1", got.ToolCallID)
	}
	if got.ToolName == nil || *got.ToolName != "edit" {
		t.Fatalf("tool_name = %v, want edit", got.ToolName)
	}
}
