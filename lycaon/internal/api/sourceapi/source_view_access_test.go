package sourceapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceViewWorkspaceMismatchReturnsExpired(t *testing.T) {
	server := newSourceHandlerFixture(t)
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root, "src"), 0o700))
	testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600))
	proj, err := project.CreateWithRoot(t.Context(), server.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	physical, err := server.Workspace.ProjectRegistry.Get(t.Context(), proj.ID)
	testutil.FailErr(t, "resolve workspace", err)

	request := wire.SourceTreeViewCreate{
		Kind:        "tree",
		ClientID:    "window:main",
		OperationID: uuid.NewString(),
		WorkspaceID: physical.WorkspaceID(),
	}
	created := readSourceViewResponse(t, callSourceViewHandler(t, server.Views.HandleCreateSourceView, proj.ID, "", request), http.StatusCreated).Tree
	testutil.WaitFor(t, 5*time.Second, func() bool {
		state := readSourceViewResponse(t, callSourceViewHandler(t, server.Views.HandleGetSourceView, proj.ID, created.ID, nil), http.StatusOK).Tree
		return state.State == "ready"
	})

	// Add a new root to change the physical workspace identity.
	secondRoot := t.TempDir()
	_, err = server.Workspace.ProjectRegistry.AttachRoot(t.Context(), proj.ID, project.AttachRootParams{Path: secondRoot})
	testutil.FailErr(t, "attach root", err)

	updatedProject, err := server.Workspace.ProjectRegistry.Get(t.Context(), proj.ID)
	testutil.FailErr(t, "get updated project", err)
	if updatedProject.WorkspaceID() == physical.WorkspaceID() {
		t.Fatal("expected workspace ID to change after adding root")
	}

	// A view from another workspace identity is no longer addressable.
	response := callSourceViewHandler(t, server.Views.HandleGetSourceView, proj.ID, created.ID, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", response.Code, response.Body.String())
	}
	var apiErr wire.ErrorResponse
	testutil.FailErr(t, "decode error response", json.Unmarshal(response.Body.Bytes(), &apiErr))
	if apiErr.Code != "source_view_not_found" {
		t.Fatalf("expected code source_view_not_found, got %q", apiErr.Code)
	}

	// A released view stays unaddressable.
	nextResponse := callSourceViewHandler(t, server.Views.HandleGetSourceView, proj.ID, created.ID, nil)
	if nextResponse.Code != http.StatusNotFound {
		t.Fatalf("expected subsequent status 404, got %d", nextResponse.Code)
	}
}
