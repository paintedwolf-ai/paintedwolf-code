package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/internal/visual"
)

type storageUsageFailStore struct{ visual.Store }

func (storageUsageFailStore) StorageUsage(context.Context, string) (storageusage.Usage, error) {
	return storageusage.Usage{}, errors.New("artifact usage unavailable")
}

type recordingInventoryService struct {
	mu       sync.Mutex
	requests []sourceledger.InventoryRequest
}

func (*recordingInventoryService) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}

func (f *recordingInventoryService) EnsureInventory(
	_ context.Context,
	req sourceledger.InventoryRequest,
) error {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return nil
}

func (*recordingInventoryService) InventoryState(
	context.Context,
	string,
	sourcebranch.ID,
	int,
) (sourceledger.InventoryState, error) {
	return sourceledger.InventoryState{Phase: sourceledger.InventoryReady, Complete: true}, nil
}

func (*recordingInventoryService) ObservePaths(
	context.Context,
	string,
	[]sourceledger.RootSpec,
	[]sourceledger.PathRef,
) (int, error) {
	return 0, nil
}

func (f *recordingInventoryService) snapshot() []sourceledger.InventoryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sourceledger.InventoryRequest(nil), f.requests...)
}

func TestSourceReadEndpointsDoNotScheduleInventory(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	fake := &recordingInventoryService{}
	srv := newTestServer(t, withLedger, func(d *Dependencies) { d.SourceInventory = fake })
	p := createProjectForTest(t, srv, t.TempDir())
	// Root attach schedules its own inventory in the background.
	drainBackground(t, srv)
	scheduled := len(fake.snapshot())

	for _, path := range []string{
		"/v1/projects/" + p.ID + "/source/storage",
		"/v1/projects/" + p.ID + "/source/walk",
	} {
		req := newAuthedRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
	}
	drainBackground(t, srv)
	if requests := fake.snapshot(); len(requests) != scheduled {
		t.Fatalf("read endpoints scheduled %d source inventories", len(requests)-scheduled)
	}
}

func TestSourceStorageReportsArtifactUsageFailure(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger, func(d *Dependencies) {
		d.SourceInventory = &recordingInventoryService{}
		d.VisualStore = storageUsageFailStore{}
	})
	p := createProjectForTest(t, srv, t.TempDir())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newAuthedRequest(
		http.MethodGet,
		"/v1/projects/"+p.ID+"/source/storage",
		nil,
	))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET source storage status = %d body = %s", rec.Code, rec.Body.String())
	}
}
