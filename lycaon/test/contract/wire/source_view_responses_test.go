package contract

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

func TestComparisonViewResponseContracts(t *testing.T) {
	t.Parallel()
	const resource = "/v1/projects/{id}/source/views/{view_id}"
	const presentation = resource + "/presentations/{presentation_id}"
	tests := []struct {
		path  string
		value any
	}{
		{resource, api.SourceComparisonView{
			Kind: "comparison", ID: fixtureSourceViewID, State: "ready", IntentRevision: "intent-1", ProjectionRevision: "projection-1",
			ExpiresAt: fixtureTimeValue(), Extent: api.SourceViewExtent{Complete: true}, Intent: api.SourceComparisonIntent{Mode: "changes"},
		}},
		{presentation + "/rows", api.SourceComparisonFrame{
			Kind: "comparison", ViewID: fixtureSourceViewID, IntentRevision: "intent-1", ProjectionRevision: "projection-1",
			Extent: api.SourceViewExtent{Complete: true}, Rows: []api.SourceReaderRow{},
		}},
		{presentation + "/locate", api.SourceComparisonLocation{Kind: "comparison", ViewID: fixtureSourceViewID, ProjectionRevision: "projection-1"}},
		{presentation + "/search", api.SourceComparisonSearchPage{
			Kind: "comparison", ViewID: fixtureSourceViewID, ProjectionRevision: "projection-1", Complete: true, Matches: []api.SourceReaderMatch{},
		}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(test.value)
			contractcheck.FailErr(t, "marshal comparison publication", err)
			contractcheck.FailErr(t, "validate comparison publication", openapi.ValidateResponse(t.Context(), http.MethodGet, test.path,
				stubPathParams(test.path), http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, body))
		})
	}
}
