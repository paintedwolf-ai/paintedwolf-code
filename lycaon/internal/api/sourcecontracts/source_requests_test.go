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

	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
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
	p, err := projecttest.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
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
	case <-t.Context().Done():
		t.Fatal("trash did not start")
	}
	cancel()
	select {
	case <-returned:
	case <-t.Context().Done():
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
		case <-t.Context().Done():
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
	p, err := projecttest.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
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
	p, err := projecttest.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
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
		case <-t.Context().Done():
			t.Fatalf("states seen = %v", states)
		}
	}
	if !states["queued"] || !states["running"] {
		t.Fatalf("states seen = %v", states)
	}
}

func TestSourceRequestReplayMatchesAcceptedFirstAnswer(t *testing.T) {
	database := testdbfixture.Open(t, "source-requests-replay.db")
	srv := contractfixture.NewTestServerWithWorkflows(t, func(d *hostapi.Dependencies) { d.Core.Database = database })
	root := t.TempDir()
	p, err := projecttest.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)

	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	id := uuid.NewString()
	completed := make(chan struct{})
	var completeOnce sync.Once
	srv.Sources.Mutations.FileOperations.Observe(func(req fileops.Request) {
		if req.ID == id && req.Terminal() {
			completeOnce.Do(func() { close(completed) })
		}
	})
	srv.Sources.Mutations.FileOperations.HoldBeforeWorkForTest(func(ctx context.Context, req fileops.Request) {
		if req.ID != id {
			return
		}
		close(held)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	expired := make(chan struct{})
	close(expired)
	srv.Sources.Mutations.SyncWindow = func(context.Context) <-chan struct{} { return expired }

	body, err := json.Marshal(wire.CreateProjectSourceEntryRequest{
		OperationID: id,
		RootID:      p.Roots[0].ID,
		Path:        "created.txt",
		Kind:        "file",
	})
	testutil.FailErr(t, "encode create request", err)
	url := "/v1/projects/" + p.ID + "/source"

	first := httptest.NewRecorder()
	srv.ServeHTTP(first, contractfixture.NewAuthedRequest(http.MethodPost, url, bytes.NewReader(body)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202 (body=%s)", first.Code, first.Body.String())
	}
	var firstDTO wire.SourceOperationStatus
	testutil.FailErr(t, "decode first 202 DTO", json.Unmarshal(first.Body.Bytes(), &firstDTO))
	if firstDTO.OperationID != id {
		t.Fatalf("first DTO operation_id = %q, want %q", firstDTO.OperationID, id)
	}

	// Wait until the worker is held, then verify an in-flight replay also answers 202.
	select {
	case <-held:
	case <-t.Context().Done():
		t.Fatal("operation did not enter hold")
	}
	inFlightReplay := httptest.NewRecorder()
	srv.ServeHTTP(inFlightReplay, contractfixture.NewAuthedRequest(http.MethodPost, url, bytes.NewReader(body)))
	if inFlightReplay.Code != http.StatusAccepted {
		t.Fatalf("in-flight replay status = %d, want 202 (body=%s)", inFlightReplay.Code, inFlightReplay.Body.String())
	}

	// Let the operation complete and verify a post-completion replay still answers 202.
	unblock()
	select {
	case <-completed:
	case <-t.Context().Done():
		t.Fatal("operation did not reach terminal state")
	}
	afterReplay := httptest.NewRecorder()
	srv.ServeHTTP(afterReplay, contractfixture.NewAuthedRequest(http.MethodPost, url, bytes.NewReader(body)))
	if afterReplay.Code != http.StatusAccepted {
		t.Fatalf("post-completion replay status = %d, want 202 (body=%s)", afterReplay.Code, afterReplay.Body.String())
	}
	var afterDTO wire.SourceOperationStatus
	testutil.FailErr(t, "decode post-completion 202 DTO", json.Unmarshal(afterReplay.Body.Bytes(), &afterDTO))
	if afterDTO.OperationID != id || !afterDTO.Complete || afterDTO.State != "completed" {
		t.Fatalf("post-completion DTO = %+v, want completed operation %q", afterDTO, id)
	}

	// Simulate a host restart against the same persisted database and verify a retry still answers 202.
	restarted := contractfixture.NewTestServerWithWorkflows(t, func(d *hostapi.Dependencies) { d.Core.Database = database })
	never := make(chan struct{})
	restarted.Sources.Mutations.SyncWindow = func(context.Context) <-chan struct{} { return never }
	restartReplay := httptest.NewRecorder()
	restarted.ServeHTTP(restartReplay, contractfixture.NewAuthedRequest(http.MethodPost, url, bytes.NewReader(body)))
	if restartReplay.Code != http.StatusAccepted {
		t.Fatalf("post-restart replay status = %d, want 202 (body=%s)", restartReplay.Code, restartReplay.Body.String())
	}
	var restartDTO wire.SourceOperationStatus
	testutil.FailErr(t, "decode post-restart 202 DTO", json.Unmarshal(restartReplay.Body.Bytes(), &restartDTO))
	if restartDTO.OperationID != id || !restartDTO.Complete || restartDTO.State != "completed" {
		t.Fatalf("post-restart DTO = %+v, want completed operation %q", restartDTO, id)
	}
}

func TestSourceRequestReplayMatchesCompletedFirstAnswer(t *testing.T) {
	srv := contractfixture.NewTestServerWithWorkflows(t)
	root := t.TempDir()
	p, err := projecttest.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)

	never := make(chan struct{})
	srv.Sources.Mutations.SyncWindow = func(context.Context) <-chan struct{} { return never }

	id := uuid.NewString()
	body, err := json.Marshal(wire.CreateProjectSourceEntryRequest{
		OperationID: id,
		RootID:      p.Roots[0].ID,
		Path:        "inline.txt",
		Kind:        "file",
	})
	testutil.FailErr(t, "encode create request", err)
	url := "/v1/projects/" + p.ID + "/source"

	first := httptest.NewRecorder()
	srv.ServeHTTP(first, contractfixture.NewAuthedRequest(http.MethodPost, url, bytes.NewReader(body)))
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201 (body=%s)", first.Code, first.Body.String())
	}
	replay := httptest.NewRecorder()
	srv.ServeHTTP(replay, contractfixture.NewAuthedRequest(http.MethodPost, url, bytes.NewReader(body)))
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want 201 (body=%s)", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
}
