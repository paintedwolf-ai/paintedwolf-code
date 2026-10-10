package sourcecontracts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectSecretIgnoreWritesReviewedFileOnly(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	svc := &projectignore.SecretService{Roots: func(_ context.Context, id string) ([]projectignore.Root, error) {
		if id != p.ID {
			return nil, projectignore.ErrRootNotFound
		}
		return []projectignore.Root{{ID: p.Roots[0].ID, Path: p.Roots[0].Path}}, nil
	}}
	srv.Admin.Project.Secrets.SecretIgnores = svc
	body := wire.AddSecretIgnoreRequest{RootID: p.Roots[0].ID, Entry: wire.SecretIgnoreEntry{ID: "fa40d86b-836e-4006-b74b-63c88c889eef", Value: " public fixture ", Reason: "published example"}}
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "encode declaration", err)
	path := "/v1/projects/" + p.ID + "/secret-ignores"
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, path, bytes.NewReader(raw)))
		if response.Code != http.StatusCreated {
			t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
		}
	}
	entries, _, err := projectignore.ReadSecrets(p.Roots[0].Path)
	testutil.FailErr(t, "read file", err)
	if len(entries) != 1 || entries[0].Value != body.Entry.Value {
		t.Fatalf("file declarations=%+v", entries)
	}
	id, release := svc.Reviews.Offer(t.Context(), p.ID, "candidate fixture")
	defer release()
	svc.Protected = func(context.Context, string, string) bool { return true }
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/secret-ignore-candidates/"+id, nil))
	if response.Code != http.StatusBadRequest || bytes.Contains(response.Body.Bytes(), []byte("candidate fixture")) {
		t.Fatalf("protected review status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProjectSecretIgnoreRemovalAnswersWithCurrentDeclarations(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	root := p.Roots[0]
	svc := &projectignore.SecretService{Roots: func(_ context.Context, id string) ([]projectignore.Root, error) {
		if id != p.ID {
			return nil, projectignore.ErrRootNotFound
		}
		return []projectignore.Root{{ID: root.ID, Path: root.Path}}, nil
	}}
	srv.Admin.Project.Secrets.SecretIgnores = svc
	entry := projectignore.SecretEntry{ID: "fa40d86b-836e-4006-b74b-63c88c889eef", Value: " public fixture ", Reason: "published example"}
	testutil.FailErr(t, "declare public value", svc.Add(t.Context(), p.ID, root.ID, entry))

	base := "/v1/projects/" + p.ID + "/secret-ignores/"
	missing := httptest.NewRecorder()
	srv.ServeHTTP(missing, contractfixture.NewAuthedRequest(http.MethodDelete, base+"entry:absent?root_id="+root.ID, nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown entry status=%d body=%s", missing.Code, missing.Body.String())
	}
	missingParam := httptest.NewRecorder()
	srv.ServeHTTP(missingParam, contractfixture.NewAuthedRequest(http.MethodDelete, base+entry.ID, nil))
	if missingParam.Code != http.StatusBadRequest {
		t.Fatalf("missing param status=%d body=%s", missingParam.Code, missingParam.Body.String())
	}
	strayFolder := httptest.NewRecorder()
	srv.ServeHTTP(strayFolder, contractfixture.NewAuthedRequest(http.MethodDelete, base+entry.ID+"?root_id="+uuid.NewString(), nil))
	if strayFolder.Code != http.StatusNotFound {
		t.Fatalf("missing folder status=%d body=%s", strayFolder.Code, strayFolder.Body.String())
	}
	if entries, _, err := projectignore.ReadSecrets(root.Path); err != nil || len(entries) != 1 {
		t.Fatalf("refused removal changed the file: entries=%+v err=%v", entries, err)
	}

	response := httptest.NewRecorder()
	srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodDelete, base+entry.ID+"?root_id="+root.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("withdraw status=%d body=%s", response.Code, response.Body.String())
	}
	entries, _, err := projectignore.ReadSecrets(root.Path)
	testutil.FailErr(t, "read file", err)
	if len(entries) != 0 {
		t.Fatalf("file declarations=%+v", entries)
	}
}
