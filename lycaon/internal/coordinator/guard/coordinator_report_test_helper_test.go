package guard_test

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

func coordinatorReportJSON(t *testing.T, synthesis string, citedEvidence []guidance.CoordinatorCitedEvidence, citedURLs ...string) string {
	t.Helper()
	payload := map[string]any{"synthesis": synthesis}
	if len(citedEvidence) > 0 {
		payload["cited_evidence"] = citedEvidence
	}
	if len(citedURLs) > 0 {
		payload["cited_urls"] = citedURLs
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(b)
}
