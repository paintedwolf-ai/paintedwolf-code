package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMessageNavigationEndpointResolvesStoredDestinations(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "hidden file", os.WriteFile(filepath.Join(root, ".local.txt"), []byte("local"), 0600))
	srv := newTestServer(t)
	srv.sessions.SetProjectRegistry(srv.projectRegistry)
	sess := createSessionAtPathOnServer(t, srv, root, wire.SessionPostureBuild)
	project, err := srv.projectRegistry.Get(t.Context(), sess.ProjectID)
	testutil.FailErr(t, "project", err)
	content := "See [local](.local.txt)"
	refs := []wire.NavigationReference{{Mention: ".local.txt", ProjectID: project.ID, RootID: project.Roots[0].ID, Path: ".local.txt", Explicit: true, ID: "ref-0", Syntax: "link", Status: wire.NavigationPending}}
	testutil.FailErr(t, "assistant message", srv.sessionStore.AppendMessages(t.Context(), sess.ID, wire.Message{ID: "navigation-message", Role: wire.MessageRoleAssistant, Content: content, Visibility: wire.MessageVisibilityTranscript, NavigationRefs: refs}))
	request := wire.ResolveMessageNavigationRequest{MessageID: "navigation-message", ContentSHA256: store.NavigationContentHash(content)}
	raw, err := json.Marshal(request)
	testutil.FailErr(t, "encode request", err)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/navigation", bytes.NewReader(raw)))
	if response.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", response.Code, response.Body.String())
	}
	var result wire.MessageNavigationResponse
	testutil.FailErr(t, "decode result", json.Unmarshal(response.Body.Bytes(), &result))
	if len(result.References) != 1 || result.References[0].Status != wire.NavigationResolved {
		t.Fatalf("result=%+v", result)
	}
	request.ContentSHA256 = store.NavigationContentHash("new text")
	raw, err = json.Marshal(request)
	testutil.FailErr(t, "encode stale request", err)
	response = httptest.NewRecorder()
	srv.ServeHTTP(response, newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/navigation", bytes.NewReader(raw)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale response=%d %s", response.Code, response.Body.String())
	}
}

func TestMessageNavigationUsesWorkerTree(t *testing.T) {
	primary, branch := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "primary-only file", os.WriteFile(filepath.Join(primary, "primary.go"), nil, 0600))
	testutil.FailErr(t, "worker-only file", os.WriteFile(filepath.Join(branch, "worker.go"), nil, 0600))
	testutil.FailErr(t, "branch metadata", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: "r", Path: primary, IsPrimary: true}}, SnapshotComplete: true,
	}))
	queue := worker.NewInMemoryQueue(1)
	jobID := enqueueBranchJob(t, queue, "p", branch)
	server := newTestServer(t, func(d *Dependencies) { d.Workers = queue }).SessionAdmin
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Path: primary, IsPrimary: true}}}
	refs := []wire.NavigationReference{
		{Mention: "worker.go", ProjectID: "p", RootID: "r", Path: "worker.go", Explicit: true, ID: "ref-0", Syntax: "code", Status: wire.NavigationPending},
		{Mention: "primary.go", ProjectID: "p", RootID: "r", Path: "primary.go", Explicit: true, ID: "ref-1", Syntax: "code", Status: wire.NavigationPending},
	}
	out := server.ResolveNavigationWorkspace(t.Context(), p, wire.Message{WorkerID: jobID}, refs)
	if out[0].Status != wire.NavigationResolved || out[0].WorkerID != jobID || out[1].Status != wire.NavigationMissing {
		t.Fatalf("worker navigation=%+v", out)
	}
	refs[0].WorkerID = jobID
	copied := server.ResolveNavigationWorkspace(t.Context(), p, wire.Message{}, refs[:1])
	if copied[0].Status != wire.NavigationResolved || copied[0].WorkerID != jobID {
		t.Fatalf("copied worker destination=%+v", copied)
	}
}
