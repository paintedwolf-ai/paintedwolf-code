package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFolderDetectPreviewsEnabledExtensionSuggestions(t *testing.T) {
	srv, reg := extensionsScopeServer(t)
	root := t.TempDir()
	writeOverlay(t, root, extpacks.DeviceDesiredName, `format: 1
suggest:
  - id: acme/reviewed
    source: https://example.com/acme/reviewed.git
    version: ^1.0.0
`)

	p, err := project.CreateWithRoot(t.Context(), reg, root)
	testutil.FailErr(t, "create project", err)
	out := detectFolder(t, srv, root)
	if out.ProjectID != p.ID {
		t.Fatalf("project_id=%q, want %q", out.ProjectID, p.ID)
	}
	if out.ExtensionSuggestions == nil {
		t.Fatal("extension suggestions missing")
	}
	if out.ExtensionSuggestions.SuggestionRevision == "" {
		t.Fatal("suggestion revision missing")
	}
	if len(out.ExtensionSuggestions.Suggestions) != 1 ||
		out.ExtensionSuggestions.Suggestions[0].ID != "acme/reviewed" ||
		out.ExtensionSuggestions.Suggestions[0].Status != wire.ExtensionSuggestionAvailable {
		t.Fatalf("suggestions=%+v", out.ExtensionSuggestions.Suggestions)
	}
}

func TestFolderDetectOmitsDisabledExtensionSuggestions(t *testing.T) {
	t.Run("device", func(t *testing.T) {
		srv, _ := extensionsScopeServer(t)
		root := t.TempDir()
		writeExtensionSuggestion(t, root)
		testutil.FailErr(t, "disable device suggestions", srv.settingsSvc.TrustSurfaces.PutEnabled(
			map[string]bool{projectcontrib.SurfaceExtensionSuggestions: false}))

		if got := detectFolder(t, srv, root).ExtensionSuggestions; got != nil {
			t.Fatalf("extension_suggestions=%+v, want omitted", got)
		}
	})

	t.Run("project", func(t *testing.T) {
		srv, reg := extensionsScopeServer(t)
		root := t.TempDir()
		writeExtensionSuggestion(t, root)
		p, err := project.CreateWithRoot(t.Context(), reg, root)
		testutil.FailErr(t, "create project", err)
		_, err = reg.SetTrustEnabled(t.Context(), p.ID,
			map[string]bool{projectcontrib.SurfaceExtensionSuggestions: false})
		testutil.FailErr(t, "disable project suggestions", err)

		if got := detectFolder(t, srv, root).ExtensionSuggestions; got != nil {
			t.Fatalf("extension_suggestions=%+v, want omitted", got)
		}
	})
}

func writeExtensionSuggestion(t *testing.T, root string) {
	t.Helper()
	writeOverlay(t, root, extpacks.DeviceDesiredName, `format: 1
suggest:
  - id: acme/reviewed
    source: https://example.com/acme/reviewed.git
`)
}

func detectFolder(t *testing.T, srv *Server, path string) wire.FolderDetect {
	t.Helper()
	req := newAuthedRequest(http.MethodGet,
		"/v1/projects/detect?path="+url.QueryEscape(path), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detect status=%d body=%s", w.Code, w.Body.String())
	}
	var out wire.FolderDetect
	testutil.FailErr(t, "decode detect", json.NewDecoder(w.Body).Decode(&out))
	return out
}
