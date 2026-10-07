package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// reviewLoopFixture ties a live workflow review_loop phase to its verdict fixture, so the
// contract stays in sync with the shipped verdict_schema.
type reviewLoopFixture struct {
	workflowID string
	version    string
	phaseID    string
	key        string
	fixture    string
}

var reviewLoopFixtures = []reviewLoopFixture{
	{"options", "1.0.0", "judge", "options_judge", "options_judge.json"},
	{"security-survey", "1.0.1", "claims", "survey_claims", "survey_claims.json"},
	{"security-survey", "1.0.1", "challenge", "survey_challenged", "survey_challenged.json"},
	{"plan", "1.0.0", "review", "plan_review", "plan_review.json"},
}

func reviewLoopDef(t *testing.T, f reviewLoopFixture) workflowdef.ReviewLoopDef {
	t.Helper()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get(f.workflowID, f.version)
	testutil.FailErr(t, "manifests.Get "+f.workflowID, err)
	phase, ok := m.PhaseByID(f.phaseID)
	if !ok || phase.ReviewLoop == nil {
		t.Fatalf("%s phase %q has no review_loop", f.workflowID, f.phaseID)
	}
	if phase.ReviewLoop.EvidenceKey != f.key {
		t.Fatalf("%s.%s evidence_key = %q want %q", f.workflowID, f.phaseID, phase.ReviewLoop.EvidenceKey, f.key)
	}
	return *phase.ReviewLoop
}

// reviewLoopRules are the run rules a fixture's workflow applies: its declared
// rating, and no claims yet known, so every fixture claim carries a title.
func reviewLoopRules(t *testing.T, f reviewLoopFixture) workflow.VerdictRules {
	t.Helper()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get(f.workflowID, f.version)
	testutil.FailErr(t, "manifests.Get "+f.workflowID, err)
	return workflow.VerdictRules{Brief: m.ReportBrief()}
}

func loadVerdictFixture(t *testing.T, name string) map[string]string {
	t.Helper()
	path := filepath.Join(configlayout.FindModuleRoot(), "internal", "workflow", "testdata", "review_loop", name)
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture "+name, err)
	var out map[string]string
	testutil.FailErr(t, "unmarshal fixture "+name, json.Unmarshal(data, &out))
	return out
}

func TestReviewLoopVerdictFixturesValidateTerminal(t *testing.T) {
	for _, f := range reviewLoopFixtures {
		t.Run(f.key, func(t *testing.T) {
			def := reviewLoopDef(t, f)
			verdict := loadVerdictFixture(t, f.fixture)
			if err := workflow.ValidateReviewLoopVerdict(def, verdict, reviewLoopRules(t, f)); err != nil {
				t.Fatalf("fixture %s invalid against live schema: %v", f.fixture, err)
			}
			if !workflow.ReviewLoopVerdictTerminal(def, verdict) {
				t.Fatalf("fixture %s should be the terminal verdict (first enum value)", f.fixture)
			}
			if got := workflow.ReviewLoopVerdictEvidenceVerdict(def, verdict); got != evidence.GateVerdictApproved {
				t.Fatalf("terminal fixture %s evidence verdict = %q want approved", f.fixture, got)
			}
		})
	}
}

func TestReviewLoopVerdictRejectsMalformed(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[0]) // options judge
	base := loadVerdictFixture(t, "options_judge.json")

	t.Run("off-enum verdict", func(t *testing.T) {
		v := cloneVerdict(base)
		v["verdict"] = "MAYBE"
		if err := workflow.ValidateReviewLoopVerdict(def, v, workflow.VerdictRules{}); err == nil {
			t.Fatal("expected off-enum verdict to be rejected")
		}
	})
	t.Run("empty verdict", func(t *testing.T) {
		v := cloneVerdict(base)
		delete(v, "verdict")
		if err := workflow.ValidateReviewLoopVerdict(def, v, workflow.VerdictRules{}); err == nil {
			t.Fatal("expected empty verdict to be rejected")
		}
	})
	t.Run("missing required field", func(t *testing.T) {
		v := cloneVerdict(base)
		delete(v, "winner")
		if err := workflow.ValidateReviewLoopVerdict(def, v, workflow.VerdictRules{}); err == nil {
			t.Fatal("expected missing required field to be rejected")
		}
	})
	t.Run("undeclared field", func(t *testing.T) {
		v := cloneVerdict(base)
		v["cited_evidence"] = `[{"handle":"reviewer:read#1"}]`
		if err := workflow.ValidateReviewLoopVerdict(def, v, workflow.VerdictRules{}); err == nil {
			t.Fatal("expected undeclared verdict field to be rejected")
		}
	})
}

func TestReviewLoopVerdictNonTerminalDoesNotPass(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[0]) // options judge
	v := map[string]string{
		"verdict":   "NEEDS_REVISION",
		"winner":    "undecided",
		"rationale": "the skeptic surfaced an unaddressed failure mode; re-research.",
	}
	if err := workflow.ValidateReviewLoopVerdict(def, v, workflow.VerdictRules{}); err != nil {
		t.Fatalf("NEEDS_REVISION is schema-valid but was rejected: %v", err)
	}
	if workflow.ReviewLoopVerdictTerminal(def, v) {
		t.Fatal("NEEDS_REVISION must not be terminal")
	}
	if got := workflow.ReviewLoopVerdictEvidenceVerdict(def, v); got != evidence.GateVerdictNeedsChanges {
		t.Fatalf("NEEDS_REVISION evidence verdict = %q want needs_changes", got)
	}
}

// TestReviewLoopFixturesCoverLiveKeys requires a fixture for every review_loop key a
// bundled workflow ships.
func TestReviewLoopFixturesCoverLiveKeys(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	covered := map[string]bool{}
	for _, f := range reviewLoopFixtures {
		covered[f.key] = true
	}
	for _, m := range manifests.List() {
		for _, phaseID := range m.Phases {
			phase, ok := m.PhaseByID(phaseID)
			if !ok || phase.ReviewLoop == nil {
				continue
			}
			if !covered[phase.ReviewLoop.EvidenceKey] {
				t.Fatalf("review_loop key %q (%s.%s) has no verdict fixture in reviewLoopFixtures", phase.ReviewLoop.EvidenceKey, m.ID, phaseID)
			}
		}
	}
}

func cloneVerdict(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// challengeVerdict is one challenge claim over the survey's challenge schema.
func challengeVerdict(claim string) map[string]string {
	return map[string]string{"verdict": "CHALLENGED", "challenges": "[" + claim + "]", "set_asides": "[]", "coverage": `{"revision":"fixture","assessments":[]}`}
}

// A claim's status is one of the words its phase declared; the host reads the
// class declared for the word, never the word itself.
func TestReviewLoopVerdictClaimStatusIsDeclared(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[2]) // survey challenge
	rules := workflow.VerdictRules{KnownClaims: map[string]bool{"c1": true}}
	for _, status := range []string{"survives", "Refuted", "unresolved"} {
		v := challengeVerdict(`{"id":"c1","status":"` + status + `","statement":"s","cited_evidence":[{"path":"a.go","line":1}]}`)
		if err := workflow.ValidateReviewLoopVerdict(def, v, rules); err != nil {
			t.Fatalf("declared status %q rejected: %v", status, err)
		}
	}
	for _, status := range []string{"", "confirmed"} {
		v := challengeVerdict(`{"id":"c1","status":"` + status + `","statement":"s"}`)
		if err := workflow.ValidateReviewLoopVerdict(def, v, rules); err == nil {
			t.Fatalf("status %q accepted; want only declared words", status)
		}
	}
	if got := def.ClassOf("Survives"); got != workflowdef.ClaimHeld {
		t.Fatalf("class of survives = %q, want held", got)
	}
	if got := def.ClassOf("pending"); got != workflowdef.ClaimOpen {
		t.Fatalf("class of an undeclared word = %q, want open", got)
	}
}

// A phase declaring one status word supplies it when a claim leaves status out.
func TestReviewLoopVerdictSingleStatusWordIsTheDefault(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[1]) // survey claims: claimed only
	v := map[string]string{"verdict": "CLAIMED", "claims": `[{"id":"c1","title":"Claim","statement":"s","cited_evidence":[{"handle":"read#1","path":"a.go","line":1}]}]`}
	for key, kind := range def.VerdictSchema {
		if _, ok := v[key]; ok {
			continue
		}
		v[key] = "[]"
		if kind == workflowdef.VerdictCoverageType {
			v[key] = `{"revision":"fixture","assessments":[]}`
		}
	}
	claims, err := workflow.ParseVerdictClaims(def, v)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	if got := claims["claims"][0].Status; got != def.StatusWords()[0] {
		t.Fatalf("defaulted status = %q, want %q", got, def.StatusWords()[0])
	}
	if err := workflow.ValidateReviewLoopVerdict(def, v, workflow.VerdictRules{}); err != nil {
		t.Fatalf("defaulted claim rejected: %v", err)
	}
}

// The phase that introduces a claim titles it; a later phase restating it may
// leave the title out.
func TestReviewLoopVerdictNewClaimNeedsTitle(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[2])
	untitled := challengeVerdict(`{"id":"sca-1","status":"survives","statement":"s"}`)
	if err := workflow.ValidateReviewLoopVerdict(def, untitled, workflow.VerdictRules{}); err == nil {
		t.Fatal("a new claim without a title was accepted")
	}
	known := workflow.VerdictRules{KnownClaims: map[string]bool{"sca-1": true}}
	if err := workflow.ValidateReviewLoopVerdict(def, untitled, known); err != nil {
		t.Fatalf("a restated claim needs no title: %v", err)
	}
	long := challengeVerdict(`{"id":"sca-1","title":"` + strings.Repeat("x", 121) + `","status":"survives","statement":"s"}`)
	if err := workflow.ValidateReviewLoopVerdict(def, long, workflow.VerdictRules{}); err == nil {
		t.Fatal("a title past one line was accepted")
	}
}

// Rating answers are checked against the workflow's declared questions.
func TestReviewLoopVerdictAnswersFollowTheDeclaredRating(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[2])
	rules := reviewLoopRules(t, reviewLoopFixtures[2])
	rules.KnownClaims = map[string]bool{"c1": true}
	ok := challengeVerdict(`{"id":"c1","status":"survives","statement":"s","answers":{"reachable":"unknown","outcome":"degraded","attacker":"project_files"}}`)
	if err := workflow.ValidateReviewLoopVerdict(def, ok, rules); err != nil {
		t.Fatalf("declared answers rejected: %v", err)
	}
	for _, answers := range []string{
		`{"reachable":"maybe","outcome":"degraded","attacker":"project_files"}`,
		`{"outcome":"degraded","attacker":"project_files"}`,
		`{"reachable":"reachable","outcome":"degraded","attacker":"project_files","impact":"high"}`,
		`{"reachable":"reachable","outcome":"unknown","attacker":"project_files"}`,
	} {
		v := challengeVerdict(`{"id":"c1","status":"survives","statement":"s","answers":` + answers + `}`)
		if err := workflow.ValidateReviewLoopVerdict(def, v, rules); err == nil {
			t.Fatalf("answers %s accepted", answers)
		}
	}
	if err := workflow.ValidateReviewLoopVerdict(def, ok, workflow.VerdictRules{KnownClaims: rules.KnownClaims}); err == nil {
		t.Fatal("answers accepted for a workflow that declares no rating")
	}
}
