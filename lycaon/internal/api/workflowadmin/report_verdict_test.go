package workflowadmin

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// A verdict schema belongs to the workflow that declared it: plan records
// `bullets`, options `winner` and `rationale`, the security survey
// `threat_model` and `claims`. The projection must carry each as declared.
func TestReportVerdict_ProjectsWhateverTheSchemaDeclared(t *testing.T) {
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)

	cases := map[string]struct {
		def        workflowdef.ReviewLoopDef
		artifacts  map[string]any
		decision   string
		wantFields []string
		wantClaims []string
	}{
		"options judge": {
			def: workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{
				"verdict": "SELECTED|NEEDS_REVISION", "winner": "string", "rationale": "string",
			}},
			artifacts: map[string]any{
				"verdict":   "SELECTED",
				"winner":    "A — bounded buffer",
				"rationale": "the only option whose failure mode the producer can observe",
			},
			decision:   "SELECTED",
			wantFields: []string{"rationale", "winner"},
		},
		"plan review": {
			def: workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{
				"verdict": "APPROVED|NEEDS_REVISION", "bullets": "string",
			}},
			artifacts: map[string]any{
				"verdict": "APPROVED",
				"bullets": "- sequencing holds\n- the rollback step is real",
			},
			decision:   "APPROVED",
			wantFields: []string{"bullets"},
		},
		"security survey, with typed claims": {
			def: workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{
				"verdict": "CLAIMED", "threat_model": "string", "claims": "claims",
			}},
			artifacts: map[string]any{
				"verdict":      "CLAIMED",
				"threat_model": "a public HTTP service",
				"claims": `[{"id":"app-1","statement":"token compared in variable time",` +
					`"cited_evidence":[{"path":"internal/auth/session.go","line":88}]},` +
					`{"id":"app-2","statement":"sort column reaches the query builder unvalidated"}]`,
			},
			decision:   "CLAIMED",
			wantFields: []string{"threat_model"},
			wantClaims: []string{"app-1", "app-2"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := evidence.GateRecord(
				evidence.GateTypeSurveyClaims, "phase", "run_1",
				evidence.GateVerdictPassed, tc.decision, tc.artifacts, "", "", "", 1, at,
			)

			got := reportVerdict(rec, tc.def)
			if got == nil {
				t.Fatal("want a verdict projection")
			}
			if got.Decision != tc.decision {
				t.Fatalf("decision = %q, want %q", got.Decision, tc.decision)
			}

			var names []string
			for _, f := range got.Fields {
				names = append(names, f.Name)
				if strings.TrimSpace(f.Value) == "" {
					t.Fatalf("field %q projected with no value", f.Name)
				}
			}
			if strings.Join(names, ",") != strings.Join(tc.wantFields, ",") {
				t.Fatalf("fields = %v, want %v (decision first, then alphabetical)", names, tc.wantFields)
			}

			var ids []string
			for _, c := range got.Claims {
				ids = append(ids, c.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantClaims, ",") {
				t.Fatalf("claims = %v, want %v", ids, tc.wantClaims)
			}
		})
	}
}

// The citation channels submit_verdict carries beside the verdict are not
// schema members, and a composite value is not a member's text — neither
// reaches the page whatever its key.
func TestReportVerdict_ReservedCitationChannelsAreNotFields(t *testing.T) {
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	rec := evidence.GateRecord(
		evidence.GateTypeSurveyChallenged, "challenge", "run_1", evidence.GateVerdictApproved, "CHALLENGED",
		map[string]any{
			"verdict": "CHALLENGED",
			"cited_evidence": []any{
				map[string]any{"handle": "list#2", "path": "src/pipeline.py", "line": 463, "excerpt": "decode_argv = ["},
			},
			"cited_urls": []any{"https://ffmpeg.org/security.html"},
			// A member the phase never declared, stamped as a composite: it is
			// still not a line of prose.
			"stray_object": map[string]any{"a": 1},
		},
		"", "", "", 1, at,
	)

	got := reportVerdict(rec, workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "CHALLENGED"}})
	if got == nil {
		t.Fatal("want a verdict projection")
	}
	for _, f := range got.Fields {
		if f.Name == "cited_evidence" || f.Name == "cited_urls" || f.Name == "stray_object" {
			t.Fatalf("field %q projected with value %q; reserved channels and composite values are not fields", f.Name, f.Value)
		}
		if strings.Contains(f.Value, "map[") {
			t.Fatalf("field %q printed a raw Go value: %q", f.Name, f.Value)
		}
	}
}

// A record's citation channel attributes its evidence to the phase that
// stamped it, and the URLs it cited reach the sources list under that phase.
func TestChannelCitations_AttributeToThePhase(t *testing.T) {
	arts := map[string]any{
		"verdict": "CHALLENGED",
		"cited_evidence": []any{
			map[string]any{"handle": "read#2"},
			map[string]any{"path": "internal/store/query.go", "line": float64(214)},
			map[string]any{"excerpt": "no handle, no path"},
		},
		"cited_urls": []any{"https://example.test/advisory", ""},
	}
	got := channelCitations("challenge", arts)
	if len(got) != 2 {
		t.Fatalf("citations = %+v, want the two that name a handle or a path", got)
	}
	if got[0].by != "challenge" || got[0].handle != "read#2" {
		t.Fatalf("citation[0] = %+v, want the handle attributed to the phase", got[0])
	}
	if got[1].path != "internal/store/query.go" || got[1].line != 214 {
		t.Fatalf("citation[1] = %+v, want the place it named", got[1])
	}
	if urls := artifactStrings(arts, evidence.CitedURLsArtifactKey); len(urls) != 1 || urls[0] != "https://example.test/advisory" {
		t.Fatalf("cited urls = %v, want the one non-empty URL", urls)
	}
}

// Repeating a claims-typed member as a field would print its raw JSON array
// under the claims it produced.
func TestReportVerdict_ClaimsAreNotAlsoAField(t *testing.T) {
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "CLAIMED", "claims": "claims"}}
	rec := evidence.GateRecord(
		evidence.GateTypeSurveyClaims, "phase", "run_1", evidence.GateVerdictPassed, "CLAIMED",
		map[string]any{"verdict": "CLAIMED", "claims": `[{"id":"c1","statement":"a claim"}]`},
		"", "", "", 1, at,
	)

	got := reportVerdict(rec, def)
	for _, f := range got.Fields {
		if f.Name == "claims" {
			t.Fatalf("claims projected as a field as well as claims: %q", f.Value)
		}
	}
	if len(got.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(got.Claims))
	}
	if len(got.Claims[0].CitedEvidence) != 0 {
		t.Fatalf("claim without cited_evidence projected citations: %+v", got.Claims[0].CitedEvidence)
	}
}

// A claims-typed member carries each claim's status when the phase recorded
// one, so the report can say which claims survived.
func TestReportVerdict_ClaimStatusSurvivesTheProjection(t *testing.T) {
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "CHALLENGED", "challenges": "claims"}}
	rec := evidence.GateRecord(
		evidence.GateTypeSurveyChallenged, "challenge", "run_1", evidence.GateVerdictApproved, "CHALLENGED",
		map[string]any{"verdict": "CHALLENGED", "challenges": `[{"id":"app-1","status":"survives","statement":"still reachable",` +
			`"cited_evidence":[{"handle":"read#2"}]},{"id":"app-3","status":"refuted","statement":"not exploitable"}]`},
		"", "", "", 1, at,
	)

	got := reportVerdict(rec, def)
	if len(got.Claims) != 2 {
		t.Fatalf("claims = %+v, want both challenge outcomes", got.Claims)
	}
	if got.Claims[0].Status != "survives" || got.Claims[1].Status != "refuted" {
		t.Fatalf("claim statuses = %q/%q, want survives/refuted", got.Claims[0].Status, got.Claims[1].Status)
	}
	if got.Claims[0].CitedEvidence[0].Handle != "read#2" {
		t.Fatalf("claim citation = %+v, want the handle it named", got.Claims[0].CitedEvidence[0])
	}
}

// Claims that fail to parse stay readable as an ordinary field.
func TestReportVerdict_UnparsableClaimsSurviveAsAField(t *testing.T) {
	at := time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC)
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "CLAIMED", "claims": "claims"}}
	rec := evidence.GateRecord(
		evidence.GateTypeSurveyClaims, "phase", "run_1", evidence.GateVerdictPassed, "CLAIMED",
		map[string]any{"verdict": "CLAIMED", "claims": "1. SQLi in auth.go:88"},
		"", "", "", 1, at,
	)

	got := reportVerdict(rec, def)
	if len(got.Claims) != 0 {
		t.Fatalf("claims = %+v, want none from a value that is not the claims shape", got.Claims)
	}
	if len(got.Fields) != 1 || got.Fields[0].Name != "claims" {
		t.Fatalf("fields = %+v, want the raw value kept as a field", got.Fields)
	}
}

// A record with no members at all has no adjudication to show.
func TestReportVerdict_NilWithoutADecision(t *testing.T) {
	if got := reportVerdict(evidence.Record{}, workflowdef.ReviewLoopDef{}); got != nil {
		t.Fatalf("reportVerdict = %+v, want nil without a decision", got)
	}
}
