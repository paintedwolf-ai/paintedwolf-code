package sourcecontracts

import (
	"net/http"
	"net/http/httptest"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
)

func TestSourceReadEndpointsDoNotScheduleInventory(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	fake := &contractfixture.RecordingInventoryService{}
	srv := contractfixture.NewTestServer(t, withLedger, func(d *hostapi.Dependencies) { d.Source.SourceInventory = fake })
	p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	// Root attach schedules its own inventory in the background.
	contractfixture.DrainBackground(t, srv)
	scheduled := len(fake.Snapshot())

	for _, path := range []string{
		"/v1/projects/" + p.ID + "/source/storage",
		"/v1/projects/" + p.ID + "/source/walk",
	} {
		req := contractfixture.NewAuthedRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
	}
	contractfixture.DrainBackground(t, srv)
	if requests := fake.Snapshot(); len(requests) != scheduled {
		t.Fatalf("read endpoints scheduled %d source inventories", len(requests)-scheduled)
	}
}

func TestSourceStorageReportsArtifactUsageFailure(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger, func(d *hostapi.Dependencies) {
		d.Source.SourceInventory = &contractfixture.RecordingInventoryService{}
		d.Source.VisualStore = contractfixture.StorageUsageFailStore{}
	})
	p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(
		http.MethodGet,
		"/v1/projects/"+p.ID+"/source/storage",
		nil,
	))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET source storage status = %d body = %s", rec.Code, rec.Body.String())
	}
}
