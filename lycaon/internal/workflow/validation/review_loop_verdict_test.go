package validation_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/internal/workflow/verdictcall"
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
	{"security-survey", "2.0.0", "claims", "survey_claims", "survey_claims.json"},
	{"security-survey", "2.0.0", "challenge", "survey_challenged", "survey_challenged.json"},
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
func reviewLoopRules(t *testing.T, f reviewLoopFixture) workflowvalidation.VerdictRules {
	t.Helper()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get(f.workflowID, f.version)
	testutil.FailErr(t, "manifests.Get "+f.workflowID, err)
	return workflowvalidation.VerdictRules{Brief: m.ReportBrief()}
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
			if err := workflowvalidation.ValidateReviewLoopVerdict(def, verdict, reviewLoopRules(t, f)); err != nil {
				t.Fatalf("fixture %s invalid against live schema: %v", f.fixture, err)
			}
			if !workflowvalidation.ReviewLoopVerdictTerminal(def, verdict) {
				t.Fatalf("fixture %s should be the terminal verdict (first enum value)", f.fixture)
			}
			if got := workflowvalidation.ReviewLoopVerdictEvidenceVerdict(def, verdict); got != evidence.GateVerdictApproved {
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
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, workflowvalidation.VerdictRules{}); err == nil {
			t.Fatal("expected off-enum verdict to be rejected")
		}
	})
	t.Run("empty verdict", func(t *testing.T) {
		v := cloneVerdict(base)
		delete(v, "verdict")
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, workflowvalidation.VerdictRules{}); err == nil {
			t.Fatal("expected empty verdict to be rejected")
		}
	})
	t.Run("missing required field", func(t *testing.T) {
		v := cloneVerdict(base)
		delete(v, "winner")
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, workflowvalidation.VerdictRules{}); err == nil {
			t.Fatal("expected missing required field to be rejected")
		}
	})
	t.Run("undeclared field", func(t *testing.T) {
		v := cloneVerdict(base)
		v["cited_evidence"] = `[{"handle":"reviewer:read#1"}]`
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, workflowvalidation.VerdictRules{}); err == nil {
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
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, workflowvalidation.VerdictRules{}); err != nil {
		t.Fatalf("NEEDS_REVISION is schema-valid but was rejected: %v", err)
	}
	if workflowvalidation.ReviewLoopVerdictTerminal(def, v) {
		t.Fatal("NEEDS_REVISION must not be terminal")
	}
	if got := workflowvalidation.ReviewLoopVerdictEvidenceVerdict(def, v); got != evidence.GateVerdictNeedsChanges {
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
	rules := workflowvalidation.VerdictRules{KnownClaims: map[string]bool{"c1": true}}
	for _, status := range []string{"survives", "Refuted", "unresolved"} {
		v := challengeVerdict(`{"id":"c1","status":"` + status + `","statement":"s","cited_evidence":[{"path":"a.go","line":1}]}`)
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, rules); err != nil {
			t.Fatalf("declared status %q rejected: %v", status, err)
		}
	}
	for _, status := range []string{"", "confirmed"} {
		v := challengeVerdict(`{"id":"c1","status":"` + status + `","statement":"s"}`)
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, rules); err == nil {
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
	claims, err := workflowvalidation.ParseVerdictClaims(def, v)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	if got := claims["claims"][0].Status; got != def.StatusWords()[0] {
		t.Fatalf("defaulted status = %q, want %q", got, def.StatusWords()[0])
	}
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, workflowvalidation.VerdictRules{}); err != nil {
		t.Fatalf("defaulted claim rejected: %v", err)
	}
}

// The phase that introduces a claim titles it; a later phase restating it may
// leave the title out.
func TestReviewLoopVerdictNewClaimNeedsTitle(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[2])
	untitled := challengeVerdict(`{"id":"sca-1","status":"survives","statement":"s"}`)
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, untitled, workflowvalidation.VerdictRules{}); err == nil {
		t.Fatal("a new claim without a title was accepted")
	}
	known := workflowvalidation.VerdictRules{KnownClaims: map[string]bool{"sca-1": true}}
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, untitled, known); err != nil {
		t.Fatalf("a restated claim needs no title: %v", err)
	}
	long := challengeVerdict(`{"id":"sca-1","title":"` + strings.Repeat("x", 121) + `","status":"survives","statement":"s"}`)
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, long, workflowvalidation.VerdictRules{}); err == nil {
		t.Fatal("a title past one line was accepted")
	}
}

// Rating answers are checked against the workflow's declared questions.
func TestReviewLoopVerdictAnswersFollowTheDeclaredRating(t *testing.T) {
	def := reviewLoopDef(t, reviewLoopFixtures[2])
	rules := reviewLoopRules(t, reviewLoopFixtures[2])
	rules.KnownClaims = map[string]bool{"c1": true}
	ok := challengeVerdict(`{"id":"c1","status":"survives","statement":"s","answers":{"reachable":"unknown","outcome":"degraded","attacker":"project_files"}}`)
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, ok, rules); err != nil {
		t.Fatalf("declared answers rejected: %v", err)
	}
	for _, answers := range []string{
		`{"reachable":"maybe","outcome":"degraded","attacker":"project_files"}`,
		`{"outcome":"degraded","attacker":"project_files"}`,
		`{"reachable":"reachable","outcome":"degraded","attacker":"project_files","impact":"high"}`,
		`{"reachable":"reachable","outcome":"unknown","attacker":"project_files"}`,
	} {
		v := challengeVerdict(`{"id":"c1","status":"survives","statement":"s","answers":` + answers + `}`)
		if err := workflowvalidation.ValidateReviewLoopVerdict(def, v, rules); err == nil {
			t.Fatalf("answers %s accepted", answers)
		}
	}
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, ok, workflowvalidation.VerdictRules{KnownClaims: rules.KnownClaims}); err == nil {
		t.Fatal("answers accepted for a workflow that declares no rating")
	}
}

func catalogVerdictSchema(t *testing.T) map[string]any {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	meta, ok := cfg.ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	return meta.ArgsSchema
}

// offeredVerdictSchema is the phase's call as the coordinator is offered it,
// which is also the schema admission checks.
func offeredVerdictSchema(t *testing.T, f reviewLoopFixture) map[string]any {
	t.Helper()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get(f.workflowID, f.version)
	testutil.FailErr(t, "manifests.Get "+f.workflowID, err)
	schema, err := verdictcall.Compose(catalogVerdictSchema(t), reviewLoopDef(t, f), m.ReportBrief())
	testutil.FailErr(t, "PhaseVerdictArgsSchema", err)
	return tools.TrimCoordinatorToolMeta(tools.ToolMeta{Name: "submit_verdict", ArgsSchema: schema}).ArgsSchema
}

// verdictCall turns a stored verdict back into the call that produced it.
func verdictCall(t *testing.T, def workflowdef.ReviewLoopDef, stored map[string]string) map[string]any {
	t.Helper()
	verdict := map[string]any{}
	for field, raw := range stored {
		switch def.VerdictSchema[field] {
		case workflowdef.VerdictClaimsType, workflowdef.VerdictSetAsidesType, workflowdef.VerdictCoverageType:
			var v any
			testutil.FailErr(t, "decode "+field, json.Unmarshal([]byte(raw), &v))
			verdict[field] = v
		default:
			verdict[field] = raw
		}
	}
	return map[string]any{"verdict": verdict, "cited_evidence": []any{map[string]any{"handle": "h1"}}}
}

func verdictMember(schema map[string]any, path ...string) map[string]any {
	node := schema
	for _, name := range path {
		props, _ := node["properties"].(map[string]any)
		node, _ = props[name].(map[string]any)
		if items, ok := node["items"].(map[string]any); ok && node["type"] == "array" {
			node = items
		}
	}
	return node
}

func TestPhaseVerdictSchemaAcceptsEveryShippedFixture(t *testing.T) {
	for _, f := range reviewLoopFixtures {
		t.Run(f.key, func(t *testing.T) {
			call := verdictCall(t, reviewLoopDef(t, f), loadVerdictFixture(t, f.fixture))
			if err := tools.ValidateToolArgs(offeredVerdictSchema(t, f), call); err != nil {
				t.Fatalf("a verdict the host accepts fails its phase schema: %v", err)
			}
		})
	}
}

func TestPhaseVerdictSchemaRejectsMembersNestedInCoverage(t *testing.T) {
	f := reviewLoopFixtures[1] // security survey claims
	def := reviewLoopDef(t, f)
	stored := loadVerdictFixture(t, f.fixture)
	call := verdictCall(t, def, stored)
	verdict := call["verdict"].(map[string]any)
	coverage := verdict["coverage"].(map[string]any)
	for _, field := range []string{"set_asides", "threat_model"} {
		coverage[field] = verdict[field]
		delete(verdict, field)
	}

	reject := tools.ValidateCallArguments("submit_verdict", call, offeredVerdictSchema(t, f), tools.ToolContext{})
	if reject == nil {
		t.Fatal("phase schema accepted members nested inside coverage")
	}
	got, _ := reject.Data["misplaced_fields"].([]string)
	if !slices.Equal(got, []string{"set_asides", "threat_model"}) || reject.Data["found_under"] != "verdict.coverage" || reject.Data["belongs_under"] != "verdict" {
		t.Fatalf("misplaced = %v under %v → %v", got, reject.Data["found_under"], reject.Data["belongs_under"])
	}

	raw, err := json.Marshal(coverage)
	testutil.FailErr(t, "marshal coverage", err)
	stored["coverage"] = string(raw)
	delete(stored, "set_asides")
	delete(stored, "threat_model")
	err = workflowvalidation.ValidateReviewLoopVerdict(def, stored, reviewLoopRules(t, f))
	if err == nil || !strings.Contains(err.Error(), `"set_asides" is missing`) {
		t.Fatalf("validator error = %v, want set_asides reported missing", err)
	}
}

func TestPhaseVerdictSchemaFollowsThePhaseDeclaration(t *testing.T) {
	claims := offeredVerdictSchema(t, reviewLoopFixtures[1])
	challenge := offeredVerdictSchema(t, reviewLoopFixtures[2])

	decision := verdictMember(challenge, "verdict", "verdict")
	if enum, _ := decision["enum"].([]any); len(enum) != 2 || enum[0] != "CHALLENGED" {
		t.Fatalf("challenge decision enum = %v, want the terminal value first", decision["enum"])
	}

	claim := verdictMember(claims, "verdict", "claims")
	if required, _ := claim["required"].([]any); slices.Contains(required, any("status")) {
		t.Fatal("a phase with one status word must not require status")
	}
	if _, ok := verdictMember(claims, "verdict", "claims", "question")["type"]; ok {
		t.Fatal("question offered in a phase without follow-up work")
	}
	challenged := verdictMember(challenge, "verdict", "challenges")
	if required, _ := challenged["required"].([]any); !slices.Contains(required, any("status")) {
		t.Fatal("a phase with several status words must require status")
	}
	if _, ok := verdictMember(challenge, "verdict", "challenges", "question")["type"]; !ok {
		t.Fatal("follow-up phase does not offer question")
	}

	answers := verdictMember(claims, "verdict", "claims", "answers")
	props, _ := answers["properties"].(map[string]any)
	for _, dim := range []string{"reachable", "outcome", "attacker"} {
		if _, ok := props[dim]; !ok {
			t.Fatalf("answers lacks rating question %q: %v", dim, props)
		}
	}
	reachable, _ := props["reachable"].(map[string]any)
	if enum, _ := reachable["enum"].([]any); !slices.Contains(enum, any(workflowdef.BriefUnknown)) {
		t.Fatalf("reachable enum = %v, want the declared unknown answer", enum)
	}

}

func TestPhaseVerdictOutlineNamesMembersAndTerminalValue(t *testing.T) {
	outline := verdictcall.Outline(offeredVerdictSchema(t, reviewLoopFixtures[1]))
	for _, want := range []string{"coverage: {assessments: [", "revision}", "set_asides: [", "threat_model", "verdict: CLAIMED}"} {
		if !strings.Contains(outline, want) {
			t.Fatalf("outline %q lacks %q", outline, want)
		}
	}
}

func TestEveryShippedReviewPhaseComposesItsVerdictSchema(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	catalog := catalogVerdictSchema(t)
	for _, m := range manifests.All() {
		for _, phase := range m.PhaseDefs {
			if phase.ReviewLoop == nil {
				continue
			}
			if _, err := verdictcall.Compose(catalog, *phase.ReviewLoop, m.ReportBrief()); err != nil {
				t.Errorf("%s %s phase %s: %v", m.ID, m.Version, phase.ID, err)
			}
		}
	}
}

// The claims submission that stalled a security survey run: coverage was never
// closed, so the provider passed verdict through as text and every later
// member was read inside coverage.
func TestObservedUnclosedCoverageSubmissionNamesItsDefect(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "internal", "workflow", "testdata", "review_loop", "survey_claims_unclosed_coverage.json"))
	testutil.FailErr(t, "read observed submission", err)
	var call map[string]any
	testutil.FailErr(t, "decode observed submission", json.Unmarshal(raw, &call))

	reject := tools.ValidateCallArguments("submit_verdict", call, offeredVerdictSchema(t, reviewLoopFixtures[1]), tools.ToolContext{})
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("observed submission was not refused as invalid arguments: %#v", reject)
	}
	data := reject.Data
	if data["field"] != "verdict" || data["json_malformed"] != true {
		t.Fatalf("field = %v json_malformed = %v", data["field"], data["json_malformed"])
	}
	if open, _ := data["json_open_paths"].([]string); !slices.Equal(open, []string{"verdict"}) {
		t.Fatalf("json_open_paths = %v, want [verdict]", open)
	}
	misplaced, _ := data["misplaced_fields"].([]string)
	if !slices.Equal(misplaced, []string{"set_asides", "threat_model", "verdict"}) ||
		data["found_under"] != "verdict.coverage" || data["belongs_under"] != "verdict" || data["close_before"] != "set_asides" {
		t.Fatalf("misplaced = %v under %v → %v, close before %v", misplaced, data["found_under"], data["belongs_under"], data["close_before"])
	}
	if _, ok := data["replacement_args_json"]; ok {
		t.Fatal("an 11 KB submission was restated in the refusal")
	}
}

func TestOfferedVerdictAcceptsHostFactIDsAndNestedCitations(t *testing.T) {
	f := reviewLoopFixtures[1]
	loop := reviewLoopDef(t, f)
	schema := offeredVerdictSchema(t, f)
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "execute/leg-1"}, {ID: "ingest/scans"}}, Gaps: []reviewcoverage.Fact{{ID: "gap/123"}, {ID: "question/claim-1"}, {ID: "worker/job-1/scope"}}}
	testutil.FailErr(t, "host ids fit offered schema", verdictcall.CheckCoverageIDs(schema, loop, facts))
	call := verdictCall(t, loop, loadVerdictFixture(t, f.fixture))
	verdict := call["verdict"].(map[string]any)
	coverage := verdict["coverage"].(map[string]any)
	coverage["assessments"] = []any{map[string]any{"id": "execute/leg-1", "disposition": "satisfied", "reason": "Observed trace", "cited_evidence": []any{map[string]any{"handle": "read#1"}}}}
	testutil.FailErr(t, "real host id accepted", tools.ValidateToolArgs(schema, call))
	coverage["assessments"].([]any)[0].(map[string]any)["cited_evidence"] = []any{map[string]any{"evidence": "read#1"}}
	if tools.ValidateToolArgs(schema, call) == nil {
		t.Fatal("nested citation bypassed shared schema")
	}
}
