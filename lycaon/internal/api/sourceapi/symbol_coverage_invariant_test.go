package sourceapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeclarationCoverageSurvivesSymbolFederation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "test/fixtures/search/coverage.json"))
	testutil.FailErr(t, "read shared coverage vectors", err)
	var vectors []struct {
		Reason       string
		Count, Limit int
	}
	testutil.FailErr(t, "decode coverage vectors", json.Unmarshal(raw, &vectors))
	for _, vector := range vectors {
		t.Run(vector.Reason, func(t *testing.T) {
			e, p, leg := symbolProgressFixture(t, 1)
			entry, release, err := e.progress.acquire(t.Context(), p, leg, []string{p.Roots[0].ID}, true)
			testutil.FailErr(t, "prepare discovery frontier", err)
			entry.discovery = func(context.Context, projectsource.DeclarationSearchQuery) ([]projectsource.DeclarationSearchHit, projectsource.DeclarationCoverage, error) {
				return nil, projectsource.DeclarationCoverage{Gaps: []projectsource.DeclarationGap{{Reason: projectsource.DeclarationGapReason(vector.Reason), Count: vector.Count, Limit: vector.Limit}}}, nil
			}
			release()
			result, err := search.NewRouter(nil, nil, e).Execute(t.Context(), &search.RoutedPlan{Symbol: leg}, 100)
			testutil.FailErr(t, "federate discovery coverage", err)
			found := false
			for _, issue := range result.Issues {
				if string(issue.Reason) != vector.Reason {
					continue
				}
				found = true
				if issue.Executor != search.ExecutorSymbol || issue.Count != vector.Count || issue.Limit != vector.Limit {
					t.Fatalf("coverage units changed: %+v want=%+v", issue, vector)
				}
			}
			if !found || result.Exhaustive {
				t.Fatalf("discovery gap disappeared: %+v", result)
			}
		})
	}
}

func TestCompletedSymbolForegroundStaysCompleteWhilePreparationWaits(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	root := p.Roots[0].Path
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{Lane: root, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
	testutil.FailErr(t, "hold background preparation", err)
	defer release()
	result, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "complete foreground symbols", err)
	if len(result.Hits) != 1 || len(result.Issues) != 0 {
		t.Fatalf("background preparation downgraded completed foreground work: %+v", result)
	}
}
