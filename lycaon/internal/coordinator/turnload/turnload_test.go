package turnload

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/skills"
)

func TestBundledCatalogValidates(t *testing.T) {
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if cat.Version != Version || cat.Turn.Tools.LoadAt <= 0 || cat.Turn.Guides.OmitBelow < 0 || cat.Request.MaxLoads <= 0 {
		t.Fatalf("catalog = %+v", cat)
	}
	for _, kind := range Kinds() {
		if _, ok := cat.Turn.Kind.Options[kind]; !ok {
			t.Fatalf("kind %s missing", kind)
		}
	}
}

func TestParseCatalogRejectsBadThresholds(t *testing.T) {
	bad := `version: 2
state: {user_text_chars: 900, recent_tools: 12}
turn:
  deadline_ms: 100
  tools: {question: "x", load_at: 0.5}
  guides: {question: "{description}", omit_below: 0.2, confidence_floor: 2}
  kind: {confidence_floor: 0.6, instructions: "k", options: {answer_only: a, inspect: b, change: c, run: d, delegate: e}}
request: {deadline_ms: 100, load_at: 2.5, max_loads: 12}
lookup: {deadline_ms: 100, read_at: 1}
`
	_, err := ParseCatalog([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "tools.option_words") || !strings.Contains(err.Error(), "confidence_floor") {
		t.Fatalf("err = %v", err)
	}
}

func testSpec() TurnSpec {
	return TurnSpec{
		DeadlineMS: 500,
		Tools:      ToolsSpec{Question: "which tools?", OptionWords: 6, LoadAt: 0.5},
		Guides:     GuidesSpec{Omittable: []string{"survey-ladder", "read-tool"}, Question: "which guidance?", OptionWords: 6, OmitBelow: 0.25, ConfidenceFloor: 0.6},
		Kind:       KindSpec{ConfidenceFloor: 0.6, Instructions: "kind", Options: map[string]string{KindAnswerOnly: "a", KindInspect: "b", KindChange: "c", KindRun: "d", KindDelegate: "e"}},
	}
}

func fixtureCandidates() []Candidate {
	tools := ToolCandidates([]ToolCard{{Name: "git_commit", Description: "Commit files. Replaces command: git commit."}, {Name: "command", Description: "Run a command"}})
	guides := GuideCandidates([]promptunit.Unit{{ID: "survey-ladder", Description: "orienting"}, {ID: "read-tool", Description: "reading"}})
	return Candidates(tools, guides)
}

func TestDecideLoadsToolsWithConfidenceAndOmitsGuidesWithConfidence(t *testing.T) {
	fake := &decidetest.Fake{Heads: []decide.Head{decide.HeadTurnLoad}, Answers: decide.Answers{
		"tools":  {Kind: decide.KindMulti, Probabilities: map[string]float64{"git_commit": 0.9, "command": 0.2}, Confidence: 0.85},
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0.1, "read-tool": 0.45}, Confidence: 0.7},
		"kind":   {Kind: decide.KindChoice, Choice: KindChange, Confidence: 0.8},
	}}
	got := Decide(context.Background(), fake, testSpec(), State{User: "commit"}, fixtureCandidates())
	if got.Abstained || got.Kind != KindChange {
		t.Fatalf("decision = %+v", got)
	}
	if got.Tools["git_commit"] != 0.9 || len(got.Tools) != 1 {
		t.Fatalf("tools = %v", got.Tools)
	}
	// read-tool at 0.45 is below omit_below but its certainty (0.55) is under the floor.
	if _, ok := got.Omitted["survey-ladder"]; !ok || len(got.Omitted) != 1 {
		t.Fatalf("omitted = %v; an unsure verdict must not omit", got.Omitted)
	}
	if v := got.Verdicts["tool.command"]; v.P != 0.2 || v.Confidence != 0.8 {
		t.Fatalf("verdict = %+v", v)
	}
	// One question per kind; the option text carries the unit description without the command alias.
	qs := fake.Decisions[0].Questions
	if len(qs) != 3 || qs["tools"].Kind != decide.KindMulti || qs["guides"].Kind != decide.KindMulti {
		t.Fatalf("questions = %+v", qs)
	}
	if o := qs["tools"].Options["git_commit"]; !strings.Contains(o, "Commit files") || strings.Contains(o, "Replaces command") {
		t.Fatalf("option = %q", o)
	}
	if _, ok := qs["kind"]; !ok {
		t.Fatal("kind question missing")
	}
}

func TestDecideAsksTheGuideHeadWhenTheReleaseShipsOne(t *testing.T) {
	fake := &decidetest.Fake{Answers: decide.Answers{
		"tools":  {Kind: decide.KindMulti, Probabilities: map[string]float64{"git_commit": 0.9}, Confidence: 0.9},
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0.05, "read-tool": 0.9}, Confidence: 0.95},
	}}
	spec := testSpec()
	spec.Tools.Independent = true
	spec.Guides.Omittable = []string{"survey-ladder"}
	got := Decide(context.Background(), fake, spec, State{User: "commit"}, fixtureCandidates())
	if got.Abstained || len(got.Tools) != 1 || len(got.Omitted) != 1 {
		t.Fatalf("decision = %+v", got)
	}
	if len(fake.Decisions) != 2 || fake.Decisions[0].Head != decide.HeadTurnLoad || fake.Decisions[1].Head != decide.HeadGuideLoad {
		t.Fatalf("calls = %+v", fake.Decisions)
	}
	if _, asked := fake.Decisions[0].Questions["guides"]; asked {
		t.Fatal("the tools call must not carry the guides question")
	}
	if _, asked := fake.Decisions[1].Questions["tools"]; asked || len(fake.Decisions[1].Questions) != 1 {
		t.Fatalf("guide head call = %+v", fake.Decisions[1].Questions)
	}
	// Without a guide head every question goes to turn-load in one call.
	single := &decidetest.Fake{Heads: []decide.Head{decide.HeadTurnLoad}, Answers: fake.Answers}
	Decide(context.Background(), single, spec, State{User: "commit"}, fixtureCandidates())
	if len(single.Decisions) != 1 || len(single.Decisions[0].Questions) != 2 {
		t.Fatalf("single-head calls = %+v", single.Decisions)
	}
}

func TestDecideKeepsGuidesWithoutCalibratedOmission(t *testing.T) {
	fake := &decidetest.Fake{Answers: decide.Answers{
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0, "read-tool": 0.01}, Confidence: 1},
	}}
	spec := testSpec()
	spec.Guides.Omittable = []string{"read-tool"}
	got := Decide(context.Background(), fake, spec, State{User: "commit"}, fixtureCandidates())
	if _, omitted := got.Omitted["survey-ladder"]; omitted || len(got.Omitted) != 1 {
		t.Fatalf("omitted = %v; only calibrated units may leave the prompt", got.Omitted)
	}
	if _, scored := got.Verdicts["guide.survey-ladder"]; !scored {
		t.Fatal("uncalibrated unit's score must remain available for diagnostics")
	}
	spec.Guides.Omittable = nil
	got = Decide(context.Background(), fake, spec, State{User: "commit"}, fixtureCandidates())
	if len(got.Omitted) != 0 {
		t.Fatalf("omitted = %v; no calibration keeps every unit", got.Omitted)
	}
}

func TestDecideKeepsAGuideWhoseToolTheChatAlreadyUsed(t *testing.T) {
	fake := &decidetest.Fake{Answers: decide.Answers{
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0.05, "read-tool": 0.05}, Confidence: 0.95},
	}}
	guides := GuideCandidates([]promptunit.Unit{
		{ID: "survey-ladder", Description: "orienting", NeededWith: []string{"list_dir"}},
		{ID: "read-tool", Description: "reading", Attaches: []string{"read"}},
	})
	got := Decide(context.Background(), fake, testSpec(), State{User: "commit", Loaded: []string{"read"}}, guides)
	if _, omitted := got.Omitted["read-tool"]; omitted || len(got.Omitted) != 1 {
		t.Fatalf("omitted = %v; a unit needed by a tool the chat called stays", got.Omitted)
	}
	if got.Verdicts["guide.read-tool"].P != 0.05 {
		t.Fatalf("verdicts = %v", got.Verdicts)
	}
}

func TestDecideOmitBelowZeroKeepsEveryUnit(t *testing.T) {
	fake := &decidetest.Fake{Answers: decide.Answers{
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0, "read-tool": 0.01}, Confidence: 1},
	}}
	spec := testSpec()
	spec.Guides.OmitBelow = 0
	got := Decide(context.Background(), fake, spec, State{User: "commit"}, fixtureCandidates())
	if len(got.Omitted) != 0 {
		t.Fatalf("omitted = %v; omit_below 0 keeps every unit", got.Omitted)
	}
}

func TestDecideAnswerOnlyVetoesToolsOnlyWhenTheCatalogSaysSo(t *testing.T) {
	fake := &decidetest.Fake{Answers: decide.Answers{
		"tools":  {Kind: decide.KindMulti, Probabilities: map[string]float64{"git_commit": 0.9}},
		"guides": {Kind: decide.KindMulti, Probabilities: map[string]float64{"survey-ladder": 0.1}},
		"kind":   {Kind: decide.KindChoice, Choice: KindAnswerOnly, Confidence: 0.9},
	}}
	// The kind records on the decision, and without the veto switch the tools still load.
	got := Decide(context.Background(), fake, testSpec(), State{User: "what is git"}, fixtureCandidates())
	if got.Kind != KindAnswerOnly || len(got.Tools) != 1 || len(got.Omitted) != 1 {
		t.Fatalf("decision = %+v", got)
	}
	spec := testSpec()
	spec.Kind.VetoTools = true
	got = Decide(context.Background(), fake, spec, State{User: "what is git"}, fixtureCandidates())
	if got.Kind != KindAnswerOnly || len(got.Tools) != 0 || len(got.Omitted) != 1 {
		t.Fatalf("vetoed decision = %+v", got)
	}
}

func TestDecideAbstainsOnFaultsAndAbsence(t *testing.T) {
	if got := Decide(context.Background(), decide.Absent{}, testSpec(), State{}, fixtureCandidates()); !got.Abstained || len(got.Tools) != 0 || len(got.Omitted) != 0 {
		t.Fatalf("absent = %+v", got)
	}
	fake := &decidetest.Fake{Err: decide.ErrDeadline}
	if got := Decide(context.Background(), fake, testSpec(), State{}, fixtureCandidates()); !got.Abstained || got.Reason != "deadline exceeded" {
		t.Fatalf("deadline = %+v", got)
	}
	fake = &decidetest.Fake{Err: errors.New("boom")}
	if got := Decide(context.Background(), fake, testSpec(), State{}, fixtureCandidates()); !got.Abstained || !strings.Contains(got.Reason, "boom") {
		t.Fatalf("fault = %+v", got)
	}
}

func TestResolveRequestPrefersExactNamesThenEngine(t *testing.T) {
	cards := []ToolCard{{Name: "git_compare", Description: "Compare two branches"}, {Name: "git_commit", Description: "Commit staged files"}, {Name: "http_request", Description: "Call an HTTP endpoint"}}
	spec := RequestSpec{DeadlineMS: 500, LoadAt: 2.5, MaxLoads: 2}
	fake := &decidetest.Fake{Scores: []float64{1.0, 3.5}}
	got := ResolveRequest(context.Background(), fake, spec, "git_compare the branches and then commit", cards)
	if strings.Join(got.Exact, ",") != "git_compare" || got.Abstained {
		t.Fatalf("outcome = %+v", got)
	}
	if _, ok := got.Ranked["http_request"]; !ok || len(got.Ranked) != 1 {
		t.Fatalf("ranked = %v (engine scored the rest in card order)", got.Ranked)
	}
	if strings.Join(got.Loaded(), ",") != "git_compare,http_request" {
		t.Fatalf("loaded = %v", got.Loaded())
	}
	// Without an engine, only an explicit identifier resolves.
	got = ResolveRequest(context.Background(), decide.Absent{}, spec, "call the endpoint", cards)
	if !got.Abstained || len(got.Loaded()) != 0 {
		t.Fatalf("free-text fallback = %+v", got)
	}
	got = ResolveRequest(context.Background(), decide.Absent{}, spec, "use http_request", cards)
	if !got.Abstained || strings.Join(got.Loaded(), ",") != "http_request" {
		t.Fatalf("exact fallback = %+v", got)
	}
	if got := ResolveRequest(context.Background(), fake, spec, "", cards); !got.Abstained || len(got.Loaded()) != 0 {
		t.Fatalf("empty need = %+v", got)
	}
}

func TestResolveRequestLoadsTheNearestToolsWhenNoneReachesTheBar(t *testing.T) {
	cards := []ToolCard{{Name: "view_image", Description: "Inspect an image"}, {Name: "measure_page", Description: "Measure selector geometry"}, {Name: "git_commit", Description: "Commit staged files"}}
	spec := RequestSpec{DeadlineMS: 500, LoadAt: 2.3, MaxLoads: 12, NearestLoads: 2}
	fake := &decidetest.Fake{Scores: []float64{2.1, 2.2, 0.4}}
	got := ResolveRequest(context.Background(), fake, spec, "look at the screenshot and measure the squares", cards)
	if len(got.Ranked) != 0 || got.Abstained {
		t.Fatalf("outcome = %+v", got)
	}
	if strings.Join(got.Loaded(), ",") != "measure_page,view_image" {
		t.Fatalf("loaded = %v, want the two nearest best first", got.Loaded())
	}
	if got.Nearest["view_image"] != 2.1 {
		t.Fatalf("nearest = %v", got.Nearest)
	}
	// A confident match leaves no nearest set.
	fake = &decidetest.Fake{Scores: []float64{2.5, 2.2, 0.4}}
	got = ResolveRequest(context.Background(), fake, spec, "look at the screenshot", cards)
	if len(got.Nearest) != 0 || strings.Join(got.Loaded(), ",") != "view_image" {
		t.Fatalf("confident outcome = %+v", got)
	}
}

func TestLookupSkills(t *testing.T) {
	roster := []skills.Skill{
		{Name: "publish-a-hugo-site", Description: "Build and launch a Hugo site locally."},
		{Name: "commit-in-logical-groups", Description: "Stage and commit related changes together."},
		{Name: "verify-a-change", Description: "Ground a change in evidence."},
	}
	spec := LookupSpec{DeadlineMS: 500, ReadAt: 2}
	fake := &decidetest.Fake{Scores: []float64{3.8, 0.5, 2.5}}
	got := LookupSkills(context.Background(), fake, spec, "launch the docs site", roster)
	if got.Abstained || strings.Join(got.Names(), ",") != "publish-a-hugo-site" {
		t.Fatalf("ranked = %+v", got)
	}
	// Without an engine, only an explicit identifier resolves.
	got = LookupSkills(context.Background(), decide.Absent{}, spec, "commit the changes", roster)
	if !got.Abstained || len(got.Names()) != 0 {
		t.Fatalf("free-text fallback = %+v", got)
	}
	got = LookupSkills(context.Background(), decide.Absent{}, spec, "commit-in-logical-groups", roster)
	if got.Abstained || strings.Join(got.Names(), ",") != "commit-in-logical-groups" {
		t.Fatalf("exact fallback = %+v", got)
	}
	if got := LookupSkills(context.Background(), fake, spec, "", roster); !got.Abstained || len(got.Names()) != 0 {
		t.Fatalf("empty need = %+v", got)
	}
	// A runner-up inside the margin makes the text fit two procedures, so none is read.
	close := &decidetest.Fake{Scores: []float64{3.8, 0.5, 3.6}}
	spec.Margin = 0.3
	if got := LookupSkills(context.Background(), close, spec, "launch the docs site", roster); got.Abstained || len(got.Names()) != 0 {
		t.Fatalf("close ranking = %+v; a margin below 0.3 must not read", got)
	}
	if got := LookupSkills(context.Background(), fake, spec, "launch the docs site", roster); strings.Join(got.Names(), ",") != "publish-a-hugo-site" {
		t.Fatalf("clear ranking = %+v", got)
	}
}

func TestToolAndSkillIdentifiersShareBoundaryMatching(t *testing.T) {
	tools := []ToolCard{{Name: "read"}, {Name: "request_tools"}}
	if got := ExactNames("Use REQUEST_TOOLS, then read the result", tools); strings.Join(got, ",") != "read,request_tools" {
		t.Fatalf("tool identifiers = %v", got)
	}
	if got := ExactNames("request_tools_extra", tools); len(got) != 0 {
		t.Fatalf("partial tool identifier = %v", got)
	}
	skills := []skills.Skill{{Name: "work-with-containers"}, {Name: "work-with-containers-extra"}}
	if got := ExactSkill("Read WORK-WITH-CONTAINERS", skills); got != "work-with-containers" {
		t.Fatalf("skill identifier = %q", got)
	}
	if got := ExactSkill("Use work-with-containers and work-with-containers-extra", skills); got != "" {
		t.Fatalf("ambiguous skill identifiers = %q", got)
	}
}

func TestBoundDescriptionAndUser(t *testing.T) {
	long := strings.Repeat("word ", CardMaxWords+5)
	if got := BoundDescription(long); len(strings.Fields(got)) != CardMaxWords {
		t.Fatalf("bound = %d words", len(strings.Fields(got)))
	}
	if got := BoundUser("héllo wörld", 5); got != "héllo" {
		t.Fatalf("BoundUser = %q", got)
	}
}
