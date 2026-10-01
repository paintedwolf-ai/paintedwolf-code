package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type blockingInventoryService struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	ctxErr  chan error
}

func (*blockingInventoryService) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}

func (f *blockingInventoryService) EnsureInventory(ctx context.Context, _ sourceledger.InventoryRequest) error {
	f.once.Do(func() { close(f.started) })
	<-f.release
	f.ctxErr <- ctx.Err()
	return nil
}

func (f *blockingInventoryService) InventoryState(
	context.Context,
	string,
	sourcebranch.ID,
	int,
) (sourceledger.InventoryState, error) {
	return sourceledger.InventoryState{Phase: sourceledger.InventoryScanning}, nil
}

func (*blockingInventoryService) ObservePaths(
	context.Context,
	string,
	[]sourceledger.RootSpec,
	[]sourceledger.PathRef,
) (int, error) {
	return 0, nil
}

func TestCreateSessionReturnsBeforeInventoryAndDetachesCancellation(t *testing.T) {
	// The project is created on the registry, not through the server: project
	// creation schedules its own inventory, and the fake must see only the
	// session's.
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	registry := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	fake := &blockingInventoryService{
		started: make(chan struct{}), release: make(chan struct{}), ctxErr: make(chan error, 1),
	}
	srv := newTestServer(t, func(d *Dependencies) {
		d.Projects = registry
		d.SourceInventory = fake
	})

	ctx, cancel := context.WithCancel(t.Context())
	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session creation waited for background source inventory")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.ID == "" || sess.Status != wire.SessionStatusPreparing {
		t.Fatalf("session = %+v, want durable preparing row", sess)
	}

	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("source inventory was not scheduled")
	}
	cancel()
	close(fake.release)
	select {
	case err := <-fake.ctxErr:
		if err != nil {
			t.Fatalf("inventory inherited request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source inventory did not finish")
	}
	drainBackground(t, srv)
}
