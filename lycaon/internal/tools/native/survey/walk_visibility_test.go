package survey

import (
	"encoding/json"
	"testing"

	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func surveyFindResults(t *testing.T, out string) []map[string]any {
	t.Helper()
	var wrap struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &wrap); err != nil {
		t.Fatalf("decode find: %v (raw=%s)", err, out)
	}
	return wrap.Results
}
