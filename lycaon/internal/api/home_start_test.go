package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/homestart"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type homeStartFixture struct {
	*Server
	db db.Handle
}

func (f homeStartFixture) sessionStoreDB() db.Handle { return f.db }

func newHomeStartServer(t *testing.T) homeStartFixture {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	// Draft workspaces live under the config dir and the process source
	// catalog indexes them; finish that before TempDir removal.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		testutil.FailErr(t, "drain source catalog", sourcecatalog.Process().Drain(ctx))
	})
	fixture := storetest.New(t)
	srv := newTestServer(t, func(d *Dependencies) {
		d.Core.Store = fixture
		d.Core.Projects = project.NewSQLRegistry(fixture.DB())
	})
	return homeStartFixture{Server: srv, db: fixture.DB()}
}

func homeStartCall(t *testing.T, srv homeStartFixture, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = newAuthedRequest(method, path, nil)
	} else {
		req = newAuthedRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeHomeStart(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) wire.HomeStart {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var out wire.HomeStart
	testutil.FailErr(t, "decode home start", json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// putBound reserves a launch and waits for its background creation to settle.
func putBound(t *testing.T, srv homeStartFixture, launchID, choices string) wire.HomeStart {
	t.Helper()
	decodeHomeStart(t, homeStartCall(t, srv, http.MethodPut, "/v1/home-starts/"+launchID, `{"choices":`+choices+`}`), http.StatusAccepted)
	drainBackground(t, srv.Server)
	return decodeHomeStart(t, homeStartCall(t, srv, http.MethodGet, "/v1/home-starts/"+launchID, ""), http.StatusOK)
}

func sendFirstPrompt(t *testing.T, srv homeStartFixture, sessionID, operationID, text string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(wire.PromptRequest{OperationID: operationID, Text: text})
	testutil.FailErr(t, "encode prompt", err)
	return homeStartCall(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/prompts", string(body))
}

func countRows(t *testing.T, srv homeStartFixture, query string, args ...any) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count rows", srv.sessionStoreDB().QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

func TestHomeStartBindsOneDraftAndChatAcrossReplays(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()

	bound := putBound(t, srv, launchID, `{"mode":"ask"}`)
	if bound.Phase != "session_bound" || bound.ProjectID == "" || bound.SessionID == "" {
		t.Fatalf("bound = %#v", bound)
	}
	if bound.SessionStatus != wire.SessionStatusIdle {
		t.Fatalf("session status = %q, want prepared idle chat", bound.SessionStatus)
	}
	if bound.FirstPrompt.OperationID != launchID || bound.FirstPrompt.Admitted {
		t.Fatalf("first prompt = %#v", bound.FirstPrompt)
	}
	for range 3 {
		again := putBound(t, srv, launchID, `{"mode":"ask"}`)
		if again.ProjectID != bound.ProjectID || again.SessionID != bound.SessionID {
			t.Fatalf("replay bound %s/%s, first %s/%s", again.ProjectID, again.SessionID, bound.ProjectID, bound.SessionID)
		}
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM projects`); got != 1 {
		t.Fatalf("projects = %d, want 1", got)
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM sessions`); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
	var mode string
	testutil.FailErr(t, "read mode", srv.sessionStoreDB().QueryRowContext(context.Background(),
		`SELECT mode FROM sessions WHERE id = ?`, bound.SessionID).Scan(&mode))
	if mode != "ask" {
		t.Fatalf("chat mode = %q, want the launch choice", mode)
	}
}

func TestHomeStartRejectsChangedChoicesUnderOneKey(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()
	putBound(t, srv, launchID, `{}`)
	rec := homeStartCall(t, srv, http.MethodPut, "/v1/home-starts/"+launchID, `{"choices":{"mode":"ask"}}`)
	assertErrorResponse(t, rec, http.StatusConflict, string(wire.ApiErrorCodeHomeStartConflict))
}

func TestHomeStartReadsAbsentKeyWithoutAllocating(t *testing.T) {
	srv := newHomeStartServer(t)
	rec := homeStartCall(t, srv, http.MethodGet, "/v1/home-starts/"+uuid.NewString(), "")
	assertErrorResponse(t, rec, http.StatusNotFound, string(wire.ApiErrorCodeHomeStartNotFound))
	if got := countRows(t, srv, `SELECT COUNT(*) FROM home_starts`); got != 0 {
		t.Fatalf("home starts = %d after a read", got)
	}
}

func TestHomeStartStopBeforePutCreatesNothing(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()
	stopped := decodeHomeStart(t, homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/stop", ""), http.StatusOK)
	if stopped.Phase != "stopped" || stopped.Choices != nil {
		t.Fatalf("stopped = %#v", stopped)
	}
	late := putBound(t, srv, launchID, `{}`)
	if late.Phase != "stopped" || late.ProjectID != "" || late.SessionID != "" {
		t.Fatalf("late put = %#v", late)
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM projects`); got != 0 {
		t.Fatalf("projects = %d after a stopped launch", got)
	}
}

func TestHomeStartStopFencesFirstPromptBeforeAdmission(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()
	bound := putBound(t, srv, launchID, `{}`)
	stopped := decodeHomeStart(t, homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/stop", ""), http.StatusOK)
	if stopped.Phase != "stopped" || stopped.SessionID != bound.SessionID {
		t.Fatalf("stopped = %#v", stopped)
	}
	rec := sendFirstPrompt(t, srv, bound.SessionID, launchID, "Build a tic tac game with AI I can beat")
	assertErrorResponse(t, rec, http.StatusConflict, string(wire.ApiErrorCodeHomeStartClosed))
	if got := countRows(t, srv, `SELECT COUNT(*) FROM prompt_submissions`); got != 0 {
		t.Fatalf("receipts = %d after a fenced first prompt", got)
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM projects`); got != 1 {
		t.Fatalf("projects = %d; Stop never deletes the created draft", got)
	}
}

func TestHomeStartAdmitsFirstPromptOnceIntoItsChat(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()
	bound := putBound(t, srv, launchID, `{}`)
	for range 2 {
		rec := sendFirstPrompt(t, srv, bound.SessionID, launchID, "Build a tic tac game with AI I can beat")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("first prompt status = %d body = %s", rec.Code, rec.Body.String())
		}
	}
	drainBackground(t, srv.Server)
	if got := countRows(t, srv, `SELECT COUNT(*) FROM prompt_submissions WHERE session_id = ?`, bound.SessionID); got != 1 {
		t.Fatalf("receipts = %d, want one first turn", got)
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM messages WHERE session_id = ? AND role = 'user'`, bound.SessionID); got != 1 {
		t.Fatalf("user rows = %d, want one", got)
	}
	read := decodeHomeStart(t, homeStartCall(t, srv, http.MethodGet, "/v1/home-starts/"+launchID, ""), http.StatusOK)
	if !read.FirstPrompt.Admitted || read.FirstPrompt.Status == "" {
		t.Fatalf("first prompt = %#v", read.FirstPrompt)
	}
	if read.Revision <= bound.Revision {
		t.Fatalf("revision %d did not advance past %d on admission", read.Revision, bound.Revision)
	}
	stopped := decodeHomeStart(t, homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/stop", ""), http.StatusOK)
	if stopped.Phase != "session_bound" {
		t.Fatalf("a Stop after admission belongs to the turn, phase = %q", stopped.Phase)
	}

	other := putBound(t, srv, uuid.NewString(), `{}`)
	rec := sendFirstPrompt(t, srv, other.SessionID, launchID, "Build a tic tac game with AI I can beat")
	if rec.Code == http.StatusAccepted {
		t.Fatalf("a reserved operation was admitted into another chat")
	}
}

func TestHomeStartReplacementRetiresTheUnadmittedOperation(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()
	bound := putBound(t, srv, launchID, `{}`)
	replacement := uuid.NewString()
	body := func(revision int64) string {
		raw, err := json.Marshal(wire.ReplaceHomeStartFirstPromptRequest{
			ExpectedRevision: revision, OperationID: launchID, ReplacementOperationID: replacement,
		})
		testutil.FailErr(t, "encode replacement", err)
		return string(raw)
	}
	stale := homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/replace-first-prompt", body(bound.Revision+5))
	assertErrorResponse(t, stale, http.StatusConflict, string(wire.ApiErrorCodeHomeStartConflict))

	replaced := decodeHomeStart(t, homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/replace-first-prompt", body(bound.Revision)), http.StatusOK)
	if replaced.FirstPrompt.OperationID != replacement {
		t.Fatalf("replaced = %#v", replaced.FirstPrompt)
	}
	repeat := decodeHomeStart(t, homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/replace-first-prompt", body(bound.Revision)), http.StatusOK)
	if repeat.FirstPrompt.OperationID != replacement || repeat.Revision != replaced.Revision {
		t.Fatalf("repeat = %#v, want recorded outcome %#v", repeat, replaced)
	}

	late := sendFirstPrompt(t, srv, bound.SessionID, launchID, "the old idea")
	assertErrorResponse(t, late, http.StatusConflict, string(wire.ApiErrorCodeHomeStartClosed))
	if rec := sendFirstPrompt(t, srv, bound.SessionID, replacement, "the edited idea"); rec.Code != http.StatusAccepted {
		t.Fatalf("replacement prompt status = %d body = %s", rec.Code, rec.Body.String())
	}
	drainBackground(t, srv.Server)

	afterAdmission, err := json.Marshal(wire.ReplaceHomeStartFirstPromptRequest{
		ExpectedRevision: replaced.Revision + 1, OperationID: replacement, ReplacementOperationID: uuid.NewString(),
	})
	testutil.FailErr(t, "encode replacement", err)
	rec := homeStartCall(t, srv, http.MethodPost, "/v1/home-starts/"+launchID+"/replace-first-prompt", string(afterAdmission))
	assertErrorResponse(t, rec, http.StatusConflict, string(wire.ApiErrorCodeHomeStartAdmitted))
}

func TestHomeStartRetiresWithItsProject(t *testing.T) {
	srv := newHomeStartServer(t)
	launchID := uuid.NewString()
	bound := putBound(t, srv, launchID, `{}`)
	testutil.FailErr(t, "delete project", srv.Admin.Project.Projects.Registry.Delete(context.Background(), bound.ProjectID))
	replay := putBound(t, srv, launchID, `{}`)
	if replay.Phase != "retired" {
		t.Fatalf("replay after deletion = %#v", replay)
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM projects`); got != 0 {
		t.Fatalf("projects = %d; a replay recreated the deleted draft", got)
	}
}

func TestHomeStartResumesInterruptedCreationAtBoot(t *testing.T) {
	srv := newHomeStartServer(t)
	store := homestart.NewStore(srv.sessionStoreDB())
	owner, err := srv.peopleStore.HostOwner(context.Background())
	testutil.FailErr(t, "host owner", err)
	launchID := uuid.NewString()
	reserved, err := store.Reserve(context.Background(), launchID, owner.ID, wire.HomeStartChoices{})
	testutil.FailErr(t, "reserve", err)
	// An interrupted attempt left the reserved draft directory behind.
	base, err := configdir.UserConfigDir()
	testutil.FailErr(t, "config dir", err)
	testutil.FailErr(t, "mkdir", os.MkdirAll(enginepaths.DraftWorkspaceUnder(base, reserved.ProjectID), 0o700))

	testutil.FailErr(t, "resume", srv.Admin.HomeStart.ResumeUnfinished(context.Background()))
	drainBackground(t, srv.Server)
	read := decodeHomeStart(t, homeStartCall(t, srv, http.MethodGet, "/v1/home-starts/"+launchID, ""), http.StatusOK)
	if read.Phase != "session_bound" || read.ProjectID != reserved.ProjectID || read.SessionID != reserved.SessionID {
		t.Fatalf("resumed = %#v, reserved %s/%s", read, reserved.ProjectID, reserved.SessionID)
	}
}

func TestHomeStartPreservesForeignContentAtTheReservedPath(t *testing.T) {
	srv := newHomeStartServer(t)
	store := homestart.NewStore(srv.sessionStoreDB())
	owner, err := srv.peopleStore.HostOwner(context.Background())
	testutil.FailErr(t, "host owner", err)
	launchID := uuid.NewString()
	reserved, err := store.Reserve(context.Background(), launchID, owner.ID, wire.HomeStartChoices{})
	testutil.FailErr(t, "reserve", err)
	base, err := configdir.UserConfigDir()
	testutil.FailErr(t, "config dir", err)
	dir := enginepaths.DraftWorkspaceUnder(base, reserved.ProjectID)
	testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o700))
	keep := filepath.Join(dir, "notes.txt")
	testutil.FailErr(t, "write", os.WriteFile(keep, []byte("keep me"), 0o600))

	read := putBound(t, srv, launchID, `{}`)
	if read.Phase != "failed" || read.Failure == nil || read.Failure.Code != homestart.FailureDraftWorkspaceOccupied || read.Failure.Resumable {
		t.Fatalf("read = %#v", read)
	}
	if raw, err := os.ReadFile(keep); err != nil || string(raw) != "keep me" {
		t.Fatalf("foreign content changed: %q %v", raw, err)
	}
	if got := countRows(t, srv, `SELECT COUNT(*) FROM projects`); got != 0 {
		t.Fatalf("projects = %d, want none at an occupied path", got)
	}
}

func TestHomeStartCapabilitiesKeepTheStoreIdentity(t *testing.T) {
	srv := newHomeStartServer(t)
	read := func() wire.HomeStartCapabilities {
		rec := homeStartCall(t, srv, http.MethodGet, "/v1/home-starts/capabilities", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var out wire.HomeStartCapabilities
		testutil.FailErr(t, "decode capabilities", json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	first, second := read(), read()
	if first.ProtocolVersion != 1 || first.StoreID == "" || first.PersonID == "" || first != second {
		t.Fatalf("capabilities = %#v then %#v", first, second)
	}
}
