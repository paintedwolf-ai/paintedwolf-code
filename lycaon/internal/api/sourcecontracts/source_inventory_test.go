package sourcecontracts

import (
	"net/http"
	"net/http/httptest"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceReadEndpointsDoNotScheduleInventory(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	setupInventory := &contractfixture.RecordingInventoryService{}
	srv := contractfixture.NewTestServer(t, withLedger, func(d *hostapi.Dependencies) { d.Source.SourceInventory = setupInventory })
	p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	// Root attach binds the source watch on the server background group; wait
	// for that binding to finish before stopping the watch and draining any
	// callback work it admitted.
	contractfixture.DrainBackground(t, srv)
	testutil.FailErr(t, "stop project source watch", sourcefeed.StopProjectWatch(t.Context(), p.ID))
	contractfixture.DrainBackground(t, srv)
	// Already scheduled attachment work holds the setup service. Record only
	// requests started by the reads, even if that work finishes later.
	readInventory := &contractfixture.RecordingInventoryService{}
	srv.Sources.Watch.SourceInventory = readInventory

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
	if requests := readInventory.Snapshot(); len(requests) != 0 {
		t.Fatalf("read endpoints scheduled %d source inventories", len(requests))
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
