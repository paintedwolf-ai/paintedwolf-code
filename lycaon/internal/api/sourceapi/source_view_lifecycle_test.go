package sourceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func sourceViewErrorCode(t *testing.T, response *httptest.ResponseRecorder, status int) wire.ApiErrorCode {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d, want %d: %s", response.Code, status, response.Body.String())
	}
	var body wire.ErrorResponse
	testutil.FailErr(t, "decode error response", json.Unmarshal(response.Body.Bytes(), &body))
	return body.Code
}

func sourceViewFixtureProject(t *testing.T, server *Handler) *project.Project {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600))
	created, err := project.CreateWithRoot(t.Context(), server.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	physical, err := server.ProjectRegistry.Get(t.Context(), created.ID)
	testutil.FailErr(t, "resolve workspace", err)
	return physical
}

func createTreeView(t *testing.T, server *Handler, p *project.Project, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return callSourceViewHandler(t, server.HandleCreateSourceView, p.ID, "", wire.SourceTreeViewCreate{
		Kind: "tree", ClientID: "window:main", OperationID: uuid.NewString(), WorkspaceID: p.WorkspaceID(), SessionID: sessionID,
	})
}

func TestSourceViewHandlesTheHostDoesNotRetainAreNotFound(t *testing.T) {
	server := newSourceHandlerFixture(t)
	p := sourceViewFixtureProject(t, server)
	if code := sourceViewErrorCode(t, callSourceViewHandler(t, server.HandleGetSourceView, p.ID, uuid.NewString(), nil), http.StatusNotFound); code != "source_view_not_found" {
		t.Fatalf("never-issued view code=%q", code)
	}
	if code := sourceViewErrorCode(t, callSourceViewHandler(t, server.HandleGetSourceView, p.ID, "not-a-handle", nil), http.StatusBadRequest); code != "invalid_request" {
		t.Fatalf("malformed view code=%q", code)
	}
	if code := sourceViewErrorCode(t, callSourceViewHandler(t, server.HandleReleaseSourceView, p.ID, uuid.NewString(), nil), http.StatusNotFound); code != "source_view_not_found" {
		t.Fatalf("release of an unretained view code=%q", code)
	}
}

func TestSourceViewsEndWithTheirChat(t *testing.T) {
	sessions := store.NewMemory()
	server := newSourceHandlerFixture(t, func(deps *Deps) { deps.SessionStore = sessions })
	p := sourceViewFixtureProject(t, server)
	chat, err := sessions.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create chat", err)
	addressed := readSourceViewResponse(t, createTreeView(t, server, p, chat.ID), http.StatusCreated).Tree
	unaddressed := readSourceViewResponse(t, createTreeView(t, server, p, ""), http.StatusCreated).Tree

	server.ReleaseChatSourceViews(chat.ID)
	if code := sourceViewErrorCode(t, callSourceViewHandler(t, server.HandleGetSourceView, p.ID, addressed.ID, nil), http.StatusNotFound); code != "source_view_not_found" {
		t.Fatalf("released chat view code=%q", code)
	}
	readSourceViewResponse(t, callSourceViewHandler(t, server.HandleGetSourceView, p.ID, unaddressed.ID, nil), http.StatusOK)

	if code := sourceViewErrorCode(t, createTreeView(t, server, p, uuid.NewString()), http.StatusNotFound); code != "session_not_found" {
		t.Fatalf("view for a deleted chat code=%q", code)
	}
}

func TestSourceViewAddressOutsideTheProjectNamesTheFolder(t *testing.T) {
	server := newSourceHandlerFixture(t)
	p := sourceViewFixtureProject(t, server)
	response := callSourceViewHandler(t, server.HandleCreateSourceView, p.ID, "", wire.SourceTreeViewCreate{
		Kind: "tree", ClientID: "window:main", OperationID: uuid.NewString(), WorkspaceID: p.WorkspaceID(),
		Intent: wire.SourceTreeIntent{Disclosures: []wire.SourceTreeDisclosure{{Address: wire.SourceTreeAddress{RootID: uuid.NewString(), Path: "."}, Open: true}}},
	})
	if code := sourceViewErrorCode(t, response, http.StatusNotFound); code != "root_not_found" {
		t.Fatalf("detached folder code=%q", code)
	}
}

func TestPresentationBelongsToOneView(t *testing.T) {
	server := newSourceHandlerFixture(t)
	service := server.sourceViewRegistry()
	scope := pagedview.Scope{Person: "person", Project: "project"}
	owner := &sourceView{id: uuid.NewString(), scope: scope}
	other := &sourceView{id: uuid.NewString(), scope: scope}
	held := &sourcePresentation{read: &sourceViewRead{id: owner.id, scope: scope}, release: func() {}, releaseView: func() {}}
	id, err := service.presentations.Put(scope, held, 1, (*sourcePresentation).close)
	testutil.FailErr(t, "retain presentation", err)

	_, release, err := server.acquireViewPresentation(owner, id)
	testutil.FailErr(t, "acquire under its own view", err)
	release()
	if _, _, err := server.acquireViewPresentation(other, id); !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("addressed under another view: %v", err)
	}
	if _, _, err := server.acquireBasisPresentation(other, id); !errors.Is(err, pagedview.ErrRevision) {
		t.Fatalf("basis from another view: %v", err)
	}
	if _, _, err := server.acquireBasisPresentation(owner, uuid.NewString()); !errors.Is(err, pagedview.ErrRevision) {
		t.Fatalf("unretained basis: %v", err)
	}
}

// relabelOnFirstRead renames a root and invalidates the project's views right
// after the first read returns the pre-rename project, as a concurrent PATCH does.
type relabelOnFirstRead struct {
	project.Registry
	change func()
}

func (r *relabelOnFirstRead) Get(ctx context.Context, id string) (*project.Project, error) {
	p, err := r.Registry.Get(ctx, id)
	if change := r.change; change != nil {
		r.change = nil
		change()
	}
	return p, err
}

func TestSourceViewCreatedAcrossARootChangeUsesTheChangedFolders(t *testing.T) {
	registry := &relabelOnFirstRead{Registry: project.NewMemoryRegistry()}
	server := newSourceHandlerFixture(t, func(deps *Deps) { deps.ProjectRegistry = registry })
	p := sourceViewFixtureProject(t, server)
	label := "renamed-root"
	registry.change = func() {
		_, err := registry.PatchRoot(t.Context(), p.ID, p.Roots[0].ID, project.PatchRootParams{Label: &label})
		testutil.FailErr(t, "rename root", err)
		server.InvalidateProjectSourceViews(p.ID)
	}
	created := readSourceViewResponse(t, createTreeView(t, server, p, ""), http.StatusCreated).Tree
	if got := created.Roots[0].Label; got != label {
		t.Fatalf("view root label=%q, want %q", got, label)
	}
	retained := readSourceViewResponse(t, callSourceViewHandler(t, server.HandleGetSourceView, p.ID, created.ID, nil), http.StatusOK).Tree
	if got := retained.Roots[0].Label; got != label {
		t.Fatalf("retained view root label=%q, want %q", got, label)
	}
}
