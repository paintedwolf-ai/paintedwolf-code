package sourcecontracts

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/desktoptrash"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceRequestStatusRetainsHistoryHead(t *testing.T) {
	entryID := uuid.NewString()
	body, err := json.Marshal(map[string]string{"expected_entry_id": entryID})
	testutil.FailErr(t, "encode history request", err)
	status := sourceapi.SourceOperationDTO(fileops.Request{Method: http.MethodPost, Body: body})
	if status.ExpectedEntryID != entryID {
		t.Fatalf("history head = %q, want %q", status.ExpectedEntryID, entryID)
	}
}

func TestSourceRequestSurvivesDisconnectAndReturnsOriginalResult(t *testing.T) {
	srv := contractfixture.NewTestServerWithWorkflows(t)
	root := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "file"), []byte("kept"), 0o600))
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	srv.Sources.Mutations.SourceMutations.Effects.SetTrashMover(func(_ context.Context, path string) (desktoptrash.Receipt, error) {
		close(started)
		<-release
		return desktoptrash.Receipt{}, os.Remove(path)
	})
	id := uuid.NewString()
	path := "/v1/projects/" + p.ID + "/source?path=file&root_id=" + p.Roots[0].ID + "&operation_id=" + id
	request := contractfixture.NewAuthedRequest(http.MethodDelete, path, nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() { srv.ServeHTTP(response, request.WithContext(ctx)); close(returned) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("trash did not start")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("disconnected request did not return")
	}
	status := httptest.NewRecorder()
	srv.ServeHTTP(status, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/operations/"+id, nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	var pending wire.SourceOperationStatus
	testutil.FailErr(t, "decode progress", json.Unmarshal(status.Body.Bytes(), &pending))
	if pending.Complete || pending.Cancelable {
		t.Fatalf("commit phase=%+v", pending)
	}
	unblock()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := srv.Sources.Mutations.FileOperations.Get(t.Context(), id)
		testutil.FailErr(t, "read operation", err)
		if state.Terminal() {
			if state.State != "completed" || state.ResponseStatus != 204 {
				t.Fatalf("result=%+v", state)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("operation did not finish")
		case <-tick.C:
		}
	}
	replay := httptest.NewRecorder()
	srv.ServeHTTP(replay, contractfixture.NewAuthedRequest(http.MethodDelete, path, nil))
	if replay.Code != http.StatusNoContent {
		t.Fatalf("replay=%d body=%s", replay.Code, replay.Body.String())
	}
	missing := httptest.NewRecorder()
	srv.ServeHTTP(missing, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/operations/"+uuid.NewString(), nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing=%d", missing.Code)
	}
}

func TestSourceRequestRetryRestoresJSONBodyMetadata(t *testing.T) {
	srv := contractfixture.NewTestServerWithWorkflows(t)
	root := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	id := uuid.NewString()
	body, err := json.Marshal(map[string]string{"operation_id": id, "root_id": p.Roots[0].ID, "from": "source", "to": "copy"})
	testutil.FailErr(t, "encode copy", err)
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/copy", bytes.NewReader(body)))
	if first.Code != http.StatusNotFound {
		t.Fatalf("missing source: %d %s", first.Code, first.Body.String())
	}
	testutil.FailErr(t, "create source", os.WriteFile(filepath.Join(root, "source"), []byte("retry content"), 0o600))
	retry := httptest.NewRecorder()
	request := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/operations/"+id+"/retry", nil)
	request.Header.Del("Content-Type")
	srv.ServeHTTP(retry, request)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(root, "copy"))
	testutil.FailErr(t, "read copied file", err)
	if string(content) != "retry content" {
		t.Fatalf("copy content: %q", content)
	}
}

func TestSourceRequestPublishesEveryPersistedState(t *testing.T) {
	hub := events.NewMemoryHub()
	srv := contractfixture.NewTestServerWithWorkflows(t, func(d *hostapi.Dependencies) { d.Host.Events = hub })
	root := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "file"), []byte("kept"), 0o600))
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsubscribe)

	id := uuid.NewString()
	path := "/v1/projects/" + p.ID + "/source?path=file&root_id=" + p.Roots[0].ID + "&operation_id=" + id
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodDelete, path, nil))
	// The handler answers the delete inline when it finishes within its wait window and
	// otherwise hands back the running request; either way every state must still publish.
	if response.Code != http.StatusNoContent && response.Code != http.StatusAccepted {
		t.Fatalf("delete=%d body=%s", response.Code, response.Body.String())
	}

	// The list endpoint is a seed; every later state reaches Den as an event.
	states := map[string]bool{}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for !states["completed"] {
		select {
		case envelope := <-stream:
			if envelope.Topic != wire.EventTopicSourceOperation {
				continue
			}
			var event wire.SourceOperationEvent
			testutil.FailErr(t, "decode event", json.Unmarshal(envelope.Data, &event))
			if event.ProjectID != p.ID || event.Operation.OperationID != id {
				t.Fatalf("event scope=%+v", event)
			}
			states[event.Operation.State] = true
		case <-deadline.C:
			t.Fatalf("states seen = %v", states)
		}
	}
	if !states["queued"] || !states["running"] {
		t.Fatalf("states seen = %v", states)
	}
}
