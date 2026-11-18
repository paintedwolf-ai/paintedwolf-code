package contract

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

// comparisonViewID addresses the comparison view the publications describe.
const comparisonViewID = "bdb147bf-b55b-4347-9422-ecc67e04a7a3"

func TestComparisonViewResponseContracts(t *testing.T) {
	t.Parallel()
	params := map[string]string{
		"id": "6f0f6ad5-6c36-4a1e-9b0e-6a1f0c9d2b11", "view_id": comparisonViewID, "presentation_id": "766ad911-5b6d-48bc-984d-a74472044253",
	}
	expires := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const resource = "/v1/projects/{id}/source/views/{view_id}"
	const presentation = resource + "/presentations/{presentation_id}"
	tests := []struct {
		path  string
		value any
	}{
		{resource, api.SourceComparisonView{
			Kind: "comparison", ID: comparisonViewID, State: "ready", IntentRevision: "intent-1", ProjectionRevision: "projection-1",
			ExpiresAt: expires, Extent: api.SourceViewExtent{Complete: true}, Intent: api.SourceComparisonIntent{Mode: "changes"},
		}},
		{presentation + "/rows", api.SourceComparisonFrame{
			Kind: "comparison", ViewID: comparisonViewID, IntentRevision: "intent-1", ProjectionRevision: "projection-1",
			Extent: api.SourceViewExtent{Complete: true}, Rows: []api.SourceReaderRow{},
		}},
		{presentation + "/locate", api.SourceComparisonLocation{Kind: "comparison", ViewID: comparisonViewID, ProjectionRevision: "projection-1"}},
		{presentation + "/search", api.SourceComparisonSearchPage{
			Kind: "comparison", ViewID: comparisonViewID, ProjectionRevision: "projection-1", Complete: true, Matches: []api.SourceReaderMatch{},
		}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(test.value)
			contractcheck.FailErr(t, "marshal comparison publication", err)
			contractcheck.FailErr(t, "validate comparison publication", openapi.ValidateResponse(t.Context(), http.MethodGet, test.path,
				params, http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, body))
		})
	}
}
