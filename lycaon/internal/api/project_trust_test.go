package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPutProjectTrustAcceptsIndependentFields(t *testing.T) {
	srv, _, registry := newSettingsTestServer(t)
	created, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)

	for name, body := range map[string]string{
		"enable surface": `{"enabled":{"agents_md":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := newAuthedRequest(http.MethodPatch, "/v1/projects/"+created.ID+"/trust", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("PATCH project trust status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPutProjectTrustRejectsEmptyUpdate(t *testing.T) {
	srv, _, registry := newSettingsTestServer(t)
	created, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)

	req := newAuthedRequest(http.MethodPatch, "/v1/projects/"+created.ID+"/trust", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty project trust update status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestProjectTrustCachedDiscoveryDoesNotCacheSwitches(t *testing.T) {
	srv, _, registry := newSettingsTestServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "write guidance", os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Guidance\n"), 0o600))
	created, err := project.CreateWithRoot(t.Context(), registry, root)
	testutil.FailErr(t, "create project", err)
	url := "/v1/projects/" + created.ID + "/trust"
	get := func() wire.ProjectTrust {
		t.Helper()
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, newAuthedRequest(http.MethodGet, url, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET trust: %d %s", response.Code, response.Body.String())
		}
		var trust wire.ProjectTrust
		testutil.FailErr(t, "decode trust", json.Unmarshal(response.Body.Bytes(), &trust))
		return trust
	}
	if !get().Surfaces[0].Applying {
		t.Fatal("initial instructions did not apply")
	}
	_, err = registry.SetTrustEnabled(t.Context(), created.ID, map[string]bool{"agents_md": false})
	testutil.FailErr(t, "disable instructions", err)
	after := get()
	if after.Surfaces[0].Count != 1 || after.Surfaces[0].Applying {
		t.Fatalf("live switches: %+v", after)
	}
}
