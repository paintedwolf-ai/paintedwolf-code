//go:build darwin || linux

package sourceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOpenedLocationSelectsOneChatWithoutReplacingTree(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(db)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(db)
		deps.SourceLedger = sourceledger.New(db, t.TempDir())
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	openChat := func(projectID string) string {
		chat, err := sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectID)
		testutil.FailErr(t, "create chat", err)
		return chat.ID
	}
	chat, other := openChat(p.ID), openChat(p.ID)
	folder := t.TempDir()
	picked := filepath.Join(folder, "picked.go")
	testutil.FailErr(t, "write picked file", os.WriteFile(picked, []byte("package main\n"), 0o600))
	opened := readOpenedFile(t, callSourceViewHandlerAs(t, ctx, server.Workspace.HandleOpenOutsideFile, p.ID, "",
		wire.OpenOutsideFileRequest{SessionID: chat, Path: picked}))
	if opened.FileID == "" || opened.Location.SessionID != chat || !opened.Location.Available {
		t.Fatalf("opened file = %+v", opened)
	}
	request := wire.SourceTreeViewCreate{
		Kind: "tree", ClientID: "window:main", OperationID: uuid.NewString(), WorkspaceID: p.WorkspaceID(),
		Intent: wire.SourceTreeIntent{ChatID: chat},
	}
	view := readSourceViewResponse(t, callSourceViewHandlerAs(t, ctx, server.Views.HandleCreateSourceView, p.ID, "", request), http.StatusCreated).Tree
	testutil.WaitFor(t, "source view ready", func() bool {
		view = readSourceViewResponse(t, callSourceViewHandlerAs(t, ctx, server.Views.HandleGetSourceView, p.ID, view.ID, nil), http.StatusOK).Tree
		return view.State == "ready"
	})
	if len(view.OutsideLocations) != 1 || view.OutsideLocations[0].ID != opened.Location.ID {
		t.Fatalf("location projection = %+v", view.OutsideLocations)
	}
	selectChat := func(sessionID string) *httptest.ResponseRecorder {
		return callSourceViewHandlerAs(t, ctx, server.Trees.HandleApplySourceViewIntent, p.ID, view.ID, wire.SourceTreeViewUpdate{
			Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: view.IntentRevision,
			Command: wire.SourceTreeCommand{SelectChat: &wire.SourceTreeSelectChat{Kind: "select_chat", ChatID: sessionID}},
		})
	}
	response := selectChat(openChat(sourceViewFixtureProject(t, server).ID))
	if code := sourceViewErrorCode(t, response, http.StatusNotFound); code != wire.ApiErrorCodeSessionNotFound {
		t.Fatalf("other project chat opened location view: %q", code)
	}
	next := readSourceViewResponse(t, selectChat(other), http.StatusOK).Tree
	if next.ID != view.ID || len(next.OutsideLocations) != 0 {
		t.Fatalf("chat selection did not preserve the project view: %+v", next)
	}
}

func TestChatLocationOpenUsesObservedAnchor(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(db)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(db)
		deps.SourceLedger = sourceledger.New(db, t.TempDir())
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	chat, err := sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create chat", err)
	dir := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "make location", os.Mkdir(dir, 0o700))
	picked := filepath.Join(dir, "picked.go")
	testutil.FailErr(t, "write picked file", os.WriteFile(picked, []byte("package main\n"), 0o600))
	opened := readOpenedFile(t, callSourceViewHandlerAs(t, ctx, server.Workspace.HandleOpenOutsideFile, p.ID, "",
		wire.OpenOutsideFileRequest{SessionID: chat.ID, Path: picked}))
	response := func(path string) *httptest.ResponseRecorder {
		return callChatLocation(t, ctx, http.MethodPost, server.Workspace.HandleOpenChatLocationFile,
			p.ID, opened.Location.ID, wire.OpenChatLocationFileRequest{Path: path})
	}
	if got := readOpenedFile(t, response("picked.go")); got.FileID != opened.FileID {
		t.Fatalf("reopening changed identity %q to %q", opened.FileID, got.FileID)
	}
	sourceViewErrorCode(t, response(picked), http.StatusBadRequest)
	testutil.FailErr(t, "move the observed folder", os.Rename(dir, dir+"-old"))
	testutil.FailErr(t, "create another folder at its path", os.Mkdir(dir, 0o700))
	testutil.FailErr(t, "write replacement file", os.WriteFile(picked, []byte("replacement\n"), 0o600))
	sourceViewErrorCode(t, response("picked.go"), http.StatusNotFound)
}

func TestPickedOutsideLocationCanBeHiddenAndShown(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(db)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(db)
		deps.SourceLedger = sourceledger.New(db, t.TempDir())
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	chat, err := sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create chat", err)

	pick := func(chatID, path string) *httptest.ResponseRecorder {
		return callSourceViewHandlerAs(t, ctx, server.Workspace.HandleOpenOutsideFile, p.ID, "",
			wire.OpenOutsideFileRequest{SessionID: chatID, Path: path})
	}
	outside := t.TempDir()
	picked := filepath.Join(outside, "notes.txt")
	testutil.FailErr(t, "write outside fixture", os.WriteFile(picked, []byte("notes"), 0o600))
	sourceViewErrorCode(t, pick(uuid.NewString(), picked), http.StatusNotFound)
	sourceViewErrorCode(t, pick(chat.ID, "notes.txt"), http.StatusBadRequest)
	sourceViewErrorCode(t, pick(chat.ID, filepath.Join(p.Roots[0].Path, "inside.txt")), http.StatusBadRequest)
	sourceViewErrorCode(t, pick(chat.ID, filepath.Join(outside, "missing.txt")), http.StatusNotFound)
	opened := readOpenedFile(t, pick(chat.ID, picked))

	update := func(id string, hidden bool) *httptest.ResponseRecorder {
		return callChatLocation(t, ctx, http.MethodPatch, server.Workspace.HandleUpdateSourceChatLocation,
			p.ID, id, wire.UpdateSourceChatLocationRequest{Hidden: &hidden})
	}
	sourceViewErrorCode(t, update(uuid.NewString(), true), http.StatusNotFound)
	for _, hidden := range []bool{true, false} {
		response := update(opened.Location.ID, hidden)
		if response.Code != http.StatusOK {
			t.Fatalf("update hidden=%t: %d %s", hidden, response.Code, response.Body.String())
		}
		var location wire.SourceChatLocation
		testutil.FailErr(t, "decode updated location", json.Unmarshal(response.Body.Bytes(), &location))
		if location.ID != opened.Location.ID || location.Hidden != hidden {
			t.Fatalf("updated location = %+v, want hidden=%t", location, hidden)
		}
		covering, found, err := server.Workspace.SourceLedger.ChatLocationCovering(ctx, chat.ID, opened.Path)
		testutil.FailErr(t, "find visible location for the opened file", err)
		if found == hidden || found && covering.ID != opened.Location.ID {
			t.Fatalf("covering location = %+v, found=%t after hidden=%t", covering, found, hidden)
		}
	}
}

func callChatLocation(t *testing.T, ctx context.Context, method string, handler http.HandlerFunc, projectID, locationID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	testutil.FailErr(t, "encode location request", err)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", projectID)
	route.URLParams.Add("location_id", locationID)
	request := httptest.NewRequestWithContext(context.WithValue(ctx, chi.RouteCtxKey, route), method, "/", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestOutsideReaderOpensARecordedCommandFile(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(db)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(db)
		deps.SourceLedger = sourceledger.New(db, t.TempDir())
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	chat, err := sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create chat", err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture directory", err)
	file := filepath.Join(dir, "outside.txt")
	testutil.FailErr(t, "write outside source", os.WriteFile(file, []byte("read me\n"), 0o600))
	rel, ok := projectroot.OutsideRel(file)
	if !ok {
		t.Fatal("fixture lacks an outside address")
	}
	create := func(source wire.CurrentComparisonSource) *httptest.ResponseRecorder {
		return callSourceViewHandlerAs(t, ctx, server.Views.HandleCreateSourceView, p.ID, "", wire.SourceComparisonViewCreate{
			Kind: "comparison", OperationID: uuid.NewString(), ClientID: "window:main",
			Source: wire.SourceComparisonSelector{Current: &source},
			Intent: wire.SourceComparisonIntent{Mode: "full"},
		})
	}
	sourceViewErrorCode(t, create(wire.CurrentComparisonSource{Kind: "current", RootID: p.Outside.ID, Path: rel}), http.StatusNotFound)
	sourceViewErrorCode(t, create(wire.CurrentComparisonSource{Kind: "current", FileID: uuid.NewString()}), http.StatusNotFound)
	ledger := server.Workspace.SourceLedger
	testutil.FailErr(t, "record command write", ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: p.Outside.ID, Path: rel},
		ProjectID:      p.ID, SessionID: chat.ID, ToolName: "command", ToolCallID: "call-1",
		Origin: wire.SourceChangeOriginAgent, Op: wire.SourceChangeOpCreate, After: []byte("read me\n"),
	}))
	tracked, err := ledger.LookupFile(ctx, sourceledger.TrackInput{ProjectID: p.ID, RootID: p.Outside.ID, Path: rel})
	testutil.FailErr(t, "resolve recorded file ID", err)
	selector := wire.CurrentComparisonSource{Kind: "current", FileID: tracked.FileID}
	view := readSourceViewResponse(t, create(selector), http.StatusCreated).Comparison
	testutil.WaitFor(t, "outside reader ready", func() bool {
		current := readSourceViewResponse(t, callSourceViewHandlerAs(t, ctx, server.Views.HandleGetSourceView, p.ID, view.ID, nil), http.StatusOK).Comparison
		if current.State == "failed" {
			t.Fatalf("outside reader failed: %+v", current.Failure)
		}
		return current.State == "ready"
	})
	moved := file + "-moved"
	testutil.FailErr(t, "rename the outside file", os.Rename(file, moved))
	testutil.FailErr(t, "record the outside move", ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: p.Outside.ID, FromPath: rel, Path: rel + "-moved"},
		ProjectID:      p.ID, SessionID: chat.ID, FileID: tracked.FileID,
		Origin: wire.SourceChangeOriginAgent, Op: wire.SourceChangeOpRename, After: []byte("read me\n"),
	}))
	stale := callSourceViewHandlerAs(t, ctx, server.Views.HandleGetSourceView, p.ID, view.ID, nil)
	if code := sourceViewErrorCode(t, stale, http.StatusConflict); code != wire.ApiErrorCodeSourceVersionChanged {
		t.Fatalf("moved source returned %q", code)
	}
	view = readSourceViewResponse(t, create(selector), http.StatusCreated).Comparison
	testutil.WaitFor(t, "reader reopens at the current address", func() bool {
		current := readSourceViewResponse(t, callSourceViewHandlerAs(t, ctx, server.Views.HandleGetSourceView, p.ID, view.ID, nil), http.StatusOK).Comparison
		if current.State == "failed" {
			t.Fatalf("reopening after the move failed: %+v", current.Failure)
		}
		return current.State == "ready" && current.Comparison != nil && current.Comparison.After != nil && current.Comparison.After.Path == rel+"-moved"
	})
	testutil.FailErr(t, "move the observed folder", os.Rename(dir, dir+"-old"))
	t.Cleanup(func() { testutil.FailErr(t, "remove moved folder", os.RemoveAll(dir+"-old")) })
	testutil.FailErr(t, "replace observed folder", os.Mkdir(dir, 0o700))
	testutil.FailErr(t, "write replacement text", os.WriteFile(moved, []byte("unexpected file"), 0o600))
	view = readSourceViewResponse(t, create(selector), http.StatusCreated).Comparison
	testutil.WaitFor(t, "reader rejects the replacement", func() bool {
		current := readSourceViewResponse(t, callSourceViewHandlerAs(t, ctx, server.Views.HandleGetSourceView, p.ID, view.ID, nil), http.StatusOK).Comparison
		if current.State == "ready" {
			t.Fatal("reader opened the replacement directory")
		}
		return current.State == "failed"
	})
}

func readOpenedFile(t *testing.T, response *httptest.ResponseRecorder) wire.OpenedOutsideFile {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("opening outside file returned %d: %s", response.Code, response.Body.String())
	}
	var opened wire.OpenedOutsideFile
	testutil.FailErr(t, "decode opened outside file", json.Unmarshal(response.Body.Bytes(), &opened))
	return opened
}
