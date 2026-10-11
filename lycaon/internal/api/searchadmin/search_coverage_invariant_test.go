package searchadmin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFederatedCoverageKeepsUnitsThroughAPIAndPages(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "test/fixtures/search/coverage.json"))
	testutil.FailErr(t, "read shared wire coverage vectors", err)
	var issues []wire.SearchIssue
	testutil.FailErr(t, "decode wire coverage vectors", json.Unmarshal(raw, &issues))
	for _, issue := range issues {
		t.Run(string(issue.Reason), func(t *testing.T) {
			response := toWireSearchResponse(&search.Result{Issues: []search.Issue{{Executor: issue.Executor, Reason: search.IssueReason(issue.Reason), Count: issue.Count, Limit: issue.Limit}}, Status: search.ResultStatusPartial, CountRelation: search.CountRelationLowerBound})
			raw, err := json.Marshal(response)
			testutil.FailErr(t, "encode API response", err)
			var received wire.SearchResponse
			testutil.FailErr(t, "decode API response", json.Unmarshal(raw, &received))
			page, err := searchResponsePage(received, 1, 0, 1, "coverage")
			testutil.FailErr(t, "page coverage response", err)
			if !reflect.DeepEqual(page.Issues, []wire.SearchIssue{issue}) || page.Exhaustive || page.CountRelation != wire.SearchCountRelationLowerBound {
				t.Fatalf("API changed typed coverage: %+v", page)
			}
		})
	}
}
