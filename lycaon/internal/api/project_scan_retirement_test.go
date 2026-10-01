package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectRemovalRetiresScansOnlyAfterLastAttachment(t *testing.T) {
	for _, mutation := range []string{"delete", "detach"} {
		t.Run(mutation, func(t *testing.T) {
			_, database, withLedger := testSourceLedger(t)
			store := scan.NewSQLStore(database)
			srv := newTestServer(t, withLedger, func(d *Dependencies) {
				d.Projects = project.NewSQLRegistry(database)
				d.Sessions.SetProjectRegistry(d.Projects)
				d.ScanCadence = scancadence.New(store, nil, nil, nil, scancfg.DefaultGatesConfig(), nil)
			})
			root := t.TempDir()
			first := createProjectForTest(t, srv, root)
			second := createProjectForTest(t, srv, root)
			drainBackground(t, srv)
			canonical := first.Roots[0].Path
			testutil.FailErr(t, "seed queued scan demand", store.UpsertSeries(t.Context(), scan.SeriesRow{
				CanonicalPath: canonical, ScannerID: "test-scanner", DesiredPaths: []string{"main.go"},
				DesiredTrigger: wire.ScanTriggerWriteBurst, DueAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}))
			remove := func(p wire.Project) {
				t.Helper()
				if mutation == "detach" {
					url := "/v1/projects/" + p.ID + "/roots/" + p.Roots[0].ID
					response := httptest.NewRecorder()
					srv.ServeHTTP(response, newAuthedRequest(http.MethodDelete, url, nil))
					if response.Code != http.StatusNoContent {
						t.Fatalf("detach status=%d body=%s", response.Code, response.Body.String())
					}
					return
				}
				reqBody, err := json.Marshal(wire.ProjectRemovalRequest{OperationID: uuid.NewString()})
				testutil.FailErr(t, "encode removal request", err)
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/removals", bytes.NewReader(reqBody)))
				if response.Code != http.StatusAccepted {
					t.Fatalf("removal status=%d body=%s", response.Code, response.Body.String())
				}
			}
			remove(first)
			rows, err := store.ListSeriesForPath(t.Context(), canonical)
			testutil.FailErr(t, "shared scan demand", err)
			if len(rows) != 1 {
				t.Fatalf("shared folder lost scan demand: %d series", len(rows))
			}
			remove(second)
			rows, err = store.ListSeriesForPath(t.Context(), canonical)
			testutil.FailErr(t, "unattached scan demand", err)
			if len(rows) != 0 {
				t.Fatalf("unattached folder retained %d scan series", len(rows))
			}
		})
	}
}
