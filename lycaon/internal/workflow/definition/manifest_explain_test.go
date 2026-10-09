package definition

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const explainScanPhase = `
id: survey
version: 1.0.0
phases:
  - id: ingest
    activity_label: Collecting scan results
    on_enter:
      obligations:
        - kind: scan
          categories: [security]
          full: true
    complete_when: gates_satisfied
    gates: ["obligation_settled:scan"]
    explain:
%s
`

func TestParseManifestYAMLPhaseExplain(t *testing.T) {
	m, err := ParseManifestYAML([]byte(explainManifest("      summary: A full scan runs first\n      body: The review starts from real results.")))
	testutil.FailErr(t, "ParseManifestYAML", err)
	ingest, ok := m.PhaseByID("ingest")
	if !ok || ingest.Explain == nil {
		t.Fatalf("ingest explain = %+v", ingest.Explain)
	}
	if ingest.Explain.Summary != "A full scan runs first" || ingest.Explain.Body != "The review starts from real results." {
		t.Fatalf("explain = %+v", *ingest.Explain)
	}
}

func TestParseManifestYAMLRejectsInvalidPhaseExplain(t *testing.T) {
	long := strings.Repeat("a", ExplainSummaryMaxRunes+1)
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing summary", body: "      body: Why.", want: "explain.summary required"},
		{name: "missing body", body: "      summary: A scan runs first", want: "explain.body required"},
		{name: "long summary", body: "      summary: " + long + "\n      body: Why.", want: "explain.summary is"},
		{name: "multiline summary", body: "      summary: \"A scan\\nruns first\"\n      body: Why.", want: "explain.summary must be one line"},
		{name: "long body", body: "      summary: A scan runs first\n      body: " + strings.Repeat("b", ExplainBodyMaxRunes+1), want: "explain.body is"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseManifestYAML([]byte(explainManifest(tt.body)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// A note covers the silence of a phase the host holds; a coordinator phase
// already has someone to talk.
func TestParseManifestYAMLRejectsExplainOnCoordinatorPhase(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: survey
version: 1.0.0
phases:
  - id: plan
    activity_label: Planning
    explain:
      summary: Planning runs next
      body: Why.
`))
	if err == nil || !strings.Contains(err.Error(), "explain requires a phase the host holds") {
		t.Fatalf("err = %v, want host-held rejection", err)
	}
}

func TestShippedSecuritySurveyExplainsItsScan(t *testing.T) {
	catalog, _, err := LoadPackManifestsForCatalog(nil)
	testutil.FailErr(t, "load catalog", err)
	m, ok := catalog[ManifestKey("security-survey", "2.0.0")]
	if !ok {
		t.Fatal("missing security-survey")
	}
	ingest, ok := m.PhaseByID("ingest")
	if !ok || ingest.Explain == nil {
		t.Fatal("security-survey ingest declares no explain note")
	}
}

func explainManifest(explain string) string {
	return strings.Replace(explainScanPhase, "%s", explain, 1)
}
