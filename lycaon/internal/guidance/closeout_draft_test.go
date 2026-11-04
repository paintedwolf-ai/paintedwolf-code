package guidance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

// spanBlock is one observed window of a read.
type spanBlock struct {
	start int
	lines []string
}

func numbered(block spanBlock) string {
	rows := make([]string, 0, len(block.lines))
	for i, text := range block.lines {
		rows = append(rows, fmt.Sprintf("%d|%s", block.start+i, text))
	}
	return strings.Join(rows, "\n")
}

// readMessages builds one read tool exchange observing blocks of path.
func readMessages(path string, blocks ...spanBlock) []api.Message {
	type rangePayload struct {
		Offset  int    `json:"offset"`
		EndLine int    `json:"end_line"`
		Content string `json:"content"`
	}
	ranges := make([]rangePayload, 0, len(blocks))
	for _, block := range blocks {
		ranges = append(ranges, rangePayload{Offset: block.start, EndLine: block.start + len(block.lines) - 1, Content: numbered(block)})
	}
	raw, err := json.Marshal(map[string]any{"path": path, "ranges": ranges})
	if err != nil {
		panic(err)
	}
	return []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "c", Args: map[string]any{"path": path}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: string(raw)}},
	}
}

func lineTexts(start, end int) spanBlock {
	block := spanBlock{start: start}
	for line := start; line <= end; line++ {
		block.lines = append(block.lines, fmt.Sprintf("content of line number %d", line))
	}
	return block
}

func ledgerOf(exchanges ...[]api.Message) evidence.Ledger {
	var messages []api.Message
	for _, exchange := range exchanges {
		messages = append(messages, exchange...)
	}
	return ledgertest.BuildFromMessages("", messages)
}

func resolveMarkdown(source string, roots evidence.CitationRoots, ev evidence.Ledger) guidance.DraftCitationResolution {
	draft := guidance.ParseCloseoutDraft(source, guidance.DraftParseOptions(roots, ev))
	return guidance.ResolveDraftCitations(roots, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
}

func onlyOutcome(t *testing.T, source string, roots evidence.CitationRoots, ev evidence.Ledger) guidance.OccurrenceResolution {
	t.Helper()
	got := resolveMarkdown(source, roots, ev)
	if len(got.Occurrences) != 1 || len(got.Issues) != 0 {
		t.Fatalf("resolution of %q = %+v", source, got)
	}
	return got.Occurrences[0]
}

func multiRootFixture(t *testing.T) evidence.CitationRoots {
	t.Helper()
	primary, secondary := t.TempDir(), t.TempDir()
	file := filepath.Join(secondary, "lib", "sec.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(file, []byte("package lib\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	return evidence.CitationRoots{ActiveRootID: "p", Roots: []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "s", Label: "shared", Path: secondary},
	}}
}

// AC-AST-03: worker namespaces, secondary roots, quoted and Unicode paths and
// extensionless names resolve against their actual observed identity.
func TestDraftResolvesObservedIdentity(t *testing.T) {
	t.Parallel()
	worker := ledgerOf(readMessages("w.go", lineTexts(1, 9)))
	merged := evidence.InitLedger()
	evidence.MergeLedger(&merged, evidence.NamespaceLedger(worker, "leg-a"))
	evidence.MergeLedger(&merged, ledgerOf(
		readMessages("docs/design notes.md", lineTexts(10, 12)),
		readMessages("docs/設計.md", lineTexts(7, 7)),
		readMessages("Dockerfile", lineTexts(1, 3)),
	))
	cases := []struct {
		source, handle, path string
		line, end            int
	}{
		{"[leg-a:read#1:3-5]", "leg-a:read#1", "w.go", 3, 5},
		{"[leg-a:read#1 w.go:4]", "leg-a:read#1", "w.go", 4, 0},
		{"[w.go:2-3]", "leg-a:read#1", "w.go", 2, 3},
		{`["docs/design notes.md":11]`, "read#1", "docs/design notes.md", 11, 0},
		{"[docs/設計.md:7]", "read#2", "docs/設計.md", 7, 0},
		{"[Dockerfile:2]", "read#3", "Dockerfile", 2, 0},
		{"[Dockerfile]", "read#3", "Dockerfile", 0, 0},
		{`["docs/design notes.md"]`, "read#1", "docs/design notes.md", 0, 0},
	}
	for _, tc := range cases {
		got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, merged)
		res := got.Resolution
		if got.Outcome != guidance.CitationGrounded || res.Handle != tc.handle || res.Path != tc.path || res.Line != tc.line || res.EndLine != tc.end {
			t.Errorf("%s: %+v", tc.source, got)
		}
	}
	roots := multiRootFixture(t)
	shared := evidence.AssembleLedger([]evidence.Record{{
		Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "@shared/lib/sec.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 20}},
	}})
	for _, source := range []string{"[@shared/lib/sec.go:12]", "[lib/sec.go:12-14]", "[@shared/lib/sec.go]"} {
		got := onlyOutcome(t, source, roots, shared)
		if got.Outcome != guidance.CitationGrounded || got.Resolution.Path != "@shared/lib/sec.go" {
			t.Errorf("%s: %+v", source, got)
		}
	}
}

// AC-AST-05: a single-segment bracket is a citation only on a ledger match.
func TestDraftSingleSegmentNeedsLedgerMatch(t *testing.T) {
	t.Parallel()
	ev := ledgerOf(readMessages("LICENSE", lineTexts(1, 3)))
	if got := resolveMarkdown("See [sic] and [draft] and `LICENSE:2`.", evidence.CitationRoots{}, ev); len(got.Occurrences) != 0 || len(got.Issues) != 0 {
		t.Fatalf("prose yielded %+v", got)
	}
	got := onlyOutcome(t, "[LICENSE]", evidence.CitationRoots{}, ev)
	if got.Outcome != guidance.CitationGrounded || got.Resolution.Path != "LICENSE" {
		t.Fatalf("LICENSE = %+v", got)
	}
	unobserved := resolveMarkdown("[LICENSE]", evidence.CitationRoots{}, evidence.InitLedger())
	if len(unobserved.Occurrences) != 0 {
		t.Fatalf("unobserved name yielded %+v", unobserved)
	}
}

// AC-AST-07: an unobserved link is rejected and nothing grants it authority.
func TestDraftWebLinks(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{{Handle: "fetch#1", Kind: "fetch", Shape: evidence.ShapeURL, URL: "https://example.test/seen"}})
	seen := onlyOutcome(t, "[spec](https://example.test/seen)", evidence.CitationRoots{}, ev)
	if seen.Outcome != guidance.CitationGrounded || seen.Kind != citationmd.KindWebLink {
		t.Fatalf("observed link = %+v", seen)
	}
	for _, source := range []string{"[spec](https://example.test/other)", "<https://example.test/other>", "see https://example.test/other now"} {
		got := onlyOutcome(t, source, evidence.CitationRoots{}, ev)
		if got.Outcome != guidance.CitationNotObserved || got.Code != guidance.SynthURLNotObservedCode || got.Offender != "https://example.test/other" {
			t.Fatalf("%s = %+v", source, got)
		}
	}
	investigate := guidance.ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate",
		guidance.ParseCloseoutDraft("<https://example.test/other> [read#9]", citationmd.Options{}), guidance.CloseoutEvidence{Ledger: ev}, nil)
	if investigate.Occurrences[0].Code != guidance.InvestURLNotObservedCode || investigate.Occurrences[1].Code != guidance.InvestHandleNotObservedCode {
		t.Fatalf("investigate codes = %+v", investigate.Occurrences)
	}
}

// AC-AST-11: pathless and zero-match handles resolve by themselves, with no
// invented line.
func TestDraftBareHandlesAreNotFabricated(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "grep#1", Kind: "grep", Shape: evidence.ShapeOpaque},
		{Handle: "command#4", Kind: "command", Shape: evidence.ShapeCommand},
		{Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque},
	})
	for _, source := range []string{"[grep#1]", "[command#4]", "[task#1]"} {
		got := onlyOutcome(t, source, evidence.CitationRoots{}, ev)
		res := got.Resolution
		if got.Outcome != guidance.CitationGrounded || res.Verdict != evidence.VerdictTraced || res.Line != 0 || res.Path != "" {
			t.Errorf("%s = %+v", source, got)
		}
	}
	if got := onlyOutcome(t, "[grep#2]", evidence.CitationRoots{}, ev); got.Outcome != guidance.CitationNotObserved || got.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("unknown handle = %+v", got)
	}
	if got := onlyOutcome(t, "[grep#1:1-2]", evidence.CitationRoots{}, ev); got.Outcome != guidance.CitationUnverifiable {
		t.Fatalf("a range on a pathless record = %+v", got)
	}
}

// AC-AST-13: the four resolver outcomes survive, with range validation added.
func TestDraftResolverOutcomes(t *testing.T) {
	t.Parallel()
	unique := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	twoFiles := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"}, readSpec{"b.ts", 3, "func NewBoard() {"})

	bound := onlyOutcome(t, `[grep#9 "func NewBoard() {"]`, evidence.CitationRoots{}, unique)
	if bound.Outcome != guidance.CitationGrounded || bound.Resolution.Verdict != evidence.VerdictBound || bound.Resolution.Handle != "read#1" {
		t.Fatalf("unique loose binding = %+v", bound)
	}
	boundRange := onlyOutcome(t, `[grep#9:12-13 "func NewBoard() {"]`, evidence.CitationRoots{}, unique)
	if boundRange.Outcome != guidance.CitationUnverifiable {
		t.Fatalf("bound record does not cover the range: %+v", boundRange)
	}
	ambiguous := onlyOutcome(t, `[grep#9 "func NewBoard() {"]`, evidence.CitationRoots{}, twoFiles)
	if !ambiguous.Rejected() {
		t.Fatalf("ambiguous binding accepted: %+v", ambiguous)
	}
	legacy := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", guidance.CoordinatorCompletionReport{
		Synthesis:     "x",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Evidence: "grep#9", Excerpt: "func NewBoard() {"}},
	}, guidance.CloseoutEvidence{Ledger: twoFiles})
	if ambiguous.Code != legacy.Code {
		t.Fatalf("draft code %q differs from existing code %q", ambiguous.Code, legacy.Code)
	}
	wrongPath := onlyOutcome(t, `[sub/other.ts:3 "func NewBoard() {"]`, evidence.CitationRoots{}, unique)
	if wrongPath.Outcome != guidance.CitationNotObserved || wrongPath.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("unobserved path = %+v", wrongPath)
	}
	if got := resolveMarkdown("[other/file.ts]", evidence.CitationRoots{}, unique); len(got.Occurrences) != 0 || len(got.Issues) != 0 {
		t.Fatalf("an unobserved bare path is prose: %+v", got)
	}
	if got := onlyOutcome(t, `["other/file.ts"]`, evidence.CitationRoots{}, unique); got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("an unobserved quoted path = %+v", got)
	}
	if got := onlyOutcome(t, "[other.ts:3]", evidence.CitationRoots{}, unique); got.Outcome != guidance.CitationNotObserved || got.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("a coordinate is always an attempt: %+v", got)
	}
	superseded := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", SupersededBy: "read#2", LineRanges: []evidence.LineRange{{Start: 1, End: 5}}},
		{Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
	})
	exact := onlyOutcome(t, "[read#1:1-5]", evidence.CitationRoots{}, superseded)
	if exact.Outcome != guidance.CitationGrounded || exact.Resolution.Handle != "read#1" {
		t.Fatalf("an explicit superseded handle keeps its identity: %+v", exact)
	}
	current := onlyOutcome(t, "[a.go:1-5]", evidence.CitationRoots{}, superseded)
	if current.Outcome != guidance.CitationGrounded || current.Resolution.Handle != "read#2" {
		t.Fatalf("a path lands on the active record: %+v", current)
	}
	bare := onlyOutcome(t, "[a.go]", evidence.CitationRoots{}, superseded)
	if bare.Resolution.Handle != "read#2" {
		t.Fatalf("a bare path binds the active record: %+v", bare)
	}
	gateOnly := evidence.AssembleLedger([]evidence.Record{{Handle: "verify#1", Kind: "verify", GateType: "verify", Path: "g/h.go"}})
	if got := onlyOutcome(t, "[g/h.go]", evidence.CitationRoots{}, gateOnly); got.Outcome != guidance.CitationUnverifiable || got.Code != guidance.SynthCitationUnverifiableCode {
		t.Fatalf("a gate record is no content observation: %+v", got)
	}
	if got := onlyOutcome(t, `["g/../../h.go"]`, evidence.CitationRoots{}, gateOnly); got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("traversal = %+v", got)
	}
	survey := evidence.AssembleLedger([]evidence.Record{{Handle: "list#1", Kind: "list", Shape: evidence.ShapeFileRegion, Path: "pkg/doc.go", Survey: true}})
	if got := onlyOutcome(t, "[pkg/doc.go]", evidence.CitationRoots{}, survey); got.Outcome != guidance.CitationSurvey || got.Rejected() {
		t.Fatalf("survey path = %+v", got)
	}
}

// Validation reads every occurrence: metadata may be bounded and deduplicated,
// outcomes may not be.
func TestDraftValidatesEveryOccurrence(t *testing.T) {
	t.Parallel()
	ev := ledgerOf(readMessages("a.go", lineTexts(1, 40)))
	var source strings.Builder
	for line := 1; line <= 30; line++ {
		fmt.Fprintf(&source, "[read#1:%d] ", line)
	}
	source.WriteString("[read#1:99] [read#1:1] [read#1:1]")
	draft := guidance.ParseCloseoutDraft(source.String(), citationmd.Options{})
	if len(draft.CitedEvidence) != 24 {
		t.Fatalf("metadata = %d", len(draft.CitedEvidence))
	}
	got := guidance.ResolveDraftCitations(evidence.CitationRoots{}, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
	if len(got.Occurrences) != 33 {
		t.Fatalf("outcomes = %d", len(got.Occurrences))
	}
	for i, occurrence := range got.Occurrences {
		if occurrence.OccurrenceIndex != i {
			t.Fatalf("occurrence %d has index %d", i, occurrence.OccurrenceIndex)
		}
	}
	if draft.Occurrences[32].RepairID != "c33" || draft.Occurrences[32].Owner != guidance.DraftFieldSynthesis {
		t.Fatalf("identity = %+v", draft.Occurrences[32])
	}
	budget := guidance.ParseCloseoutDraft("[read#1] [read#2]", citationmd.Options{MaxOccurrences: 1})
	resolved := guidance.ResolveDraftCitations(evidence.CitationRoots{}, "implement_synthesis", budget, guidance.CloseoutEvidence{Ledger: ev}, nil)
	if len(resolved.Issues) != 1 || resolved.Issues[0].Code != citationmd.IssueOccurrenceBudget {
		t.Fatalf("budget issue = %+v", resolved.Issues)
	}
}

func TestCloseoutDraftTypedFields(t *testing.T) {
	t.Parallel()
	const artifact, other = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	source := "Result [read#1:2-3] [a/b.go:4] [L](https://example.test/x) [w](c/d.go#L7-L8).\n" +
		"[verify: targeted - Checked it] [command#4]\n[artifact: " + artifact + ", " + other + "]\n[cite: c1 => read#1:2]\n"
	draft := guidance.ParseCloseoutDraft(source, citationmd.Options{})
	if draft.Synthesis != source || len(draft.Issues) != 0 {
		t.Fatalf("draft = %+v", draft)
	}
	want := []guidance.CoordinatorCitedEvidence{
		{Evidence: "read#1", Line: 2, EndLine: 3},
		{Path: "a/b.go", Line: 4},
		{Path: "c/d.go", Line: 7, EndLine: 8},
		{Evidence: "command#4"},
	}
	if fmt.Sprint(draft.CitedEvidence) != fmt.Sprint(want) {
		t.Fatalf("cited = %+v", draft.CitedEvidence)
	}
	if len(draft.CitedURLs) != 1 || draft.CitedURLs[0] != "https://example.test/x" || len(draft.ArtifactIDs) != 2 {
		t.Fatalf("urls = %v, artifacts = %v", draft.CitedURLs, draft.ArtifactIDs)
	}
	if draft.Verification == nil || *draft.Verification != (verification.Assessment{Method: "targeted", Reason: "Checked it"}) {
		t.Fatalf("verification = %+v", draft.Verification)
	}
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "z.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
		{Handle: "command#4", Kind: "command", Shape: evidence.ShapeCommand},
	})
	got := guidance.ResolveDraftCitations(evidence.CitationRoots{}, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, []string{artifact})
	byKind := map[citationmd.NodeKind][]guidance.CitationOutcome{}
	for _, occurrence := range got.Occurrences {
		byKind[occurrence.Kind] = append(byKind[occurrence.Kind], occurrence.Outcome)
	}
	if fmt.Sprint(byKind[citationmd.KindArtifact]) != fmt.Sprint([]guidance.CitationOutcome{guidance.ArtifactPresentable, guidance.ArtifactNotPresentable}) {
		t.Fatalf("artifacts = %v", byKind[citationmd.KindArtifact])
	}
	if byKind[citationmd.KindVerify][0] != guidance.DirectiveParsed || byKind[citationmd.KindRepair][0] != guidance.DirectiveParsed {
		t.Fatalf("directives = %v", byKind)
	}
	if rejected := got.Rejected(); len(rejected) != 4 {
		t.Fatalf("rejected = %+v", rejected)
	}
}

// EndLine widens the typed citation without disturbing single-line consumers.
func TestCitedEvidenceEndLine(t *testing.T) {
	t.Parallel()
	report := guidance.CoordinatorCompletionReport{Synthesis: "x", CitedEvidence: []guidance.CoordinatorCitedEvidence{
		{Path: "a.go", Line: 2},
		{Path: "a.go", Line: 2, EndLine: 3},
		{Path: "a.go", Line: 2, EndLine: 4},
		{Path: "a.go", Line: 2, EndLine: 4},
		{Path: "b.go", Line: 5, EndLine: 5},
	}}
	report.Normalize()
	if len(report.CitedEvidence) != 4 {
		t.Fatalf("normalized = %+v", report.CitedEvidence)
	}
	if last := report.CitedEvidence[3]; last.EndLine != 0 {
		t.Fatalf("one-line range = %+v", last)
	}
	raw, err := json.Marshal(report.CitedEvidence[1])
	if err != nil {
		testutil.FailErr(t, "marshal", err)
	}
	if !strings.Contains(string(raw), `"end_line":3`) {
		t.Fatalf("json = %s", raw)
	}
	single, err := json.Marshal(report.CitedEvidence[0])
	if err != nil {
		testutil.FailErr(t, "marshal", err)
	}
	if strings.Contains(string(single), "end_line") {
		t.Fatalf("json = %s", single)
	}
	ev := ledgerOf(readMessages("a.go", lineTexts(1, 3)))
	evaluate := func(cited guidance.CoordinatorCitedEvidence) guidance.CloseoutGroundingEval {
		return guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis",
			guidance.CoordinatorCompletionReport{Synthesis: "x", CitedEvidence: []guidance.CoordinatorCitedEvidence{cited}}, guidance.CloseoutEvidence{Ledger: ev})
	}
	if got := evaluate(guidance.CoordinatorCitedEvidence{Path: "a.go", Line: 2}); got.Code != "" {
		t.Fatalf("single line = %+v", got)
	}
	if got := evaluate(guidance.CoordinatorCitedEvidence{Path: "a.go", Line: 2, EndLine: 3}); got.Code != "" {
		t.Fatalf("covered range = %+v", got)
	}
	got := evaluate(guidance.CoordinatorCitedEvidence{Path: "a.go", Line: 2, EndLine: 9})
	if got.Code != guidance.SynthCitationUnverifiableCode || len(got.Offenders) != 1 || got.Offenders[0] != "a.go:2-9" {
		t.Fatalf("uncovered range = %+v", got)
	}
	for _, malformed := range []guidance.CoordinatorCitedEvidence{{Path: "a.go", EndLine: 9}, {Path: "a.go", Line: 3, EndLine: 2}} {
		if got := evaluate(malformed); got.Code != guidance.SynthCitationUnverifiableCode {
			t.Fatalf("malformed range %+v = %+v", malformed, got)
		}
	}
	if got := evaluate(guidance.CoordinatorCitedEvidence{Evidence: "read#1", Line: 2, EndLine: 9, Excerpt: "content of line number 2"}); len(got.Offenders) != 1 || !strings.Contains(got.Offenders[0], `2-9 ("content`) {
		t.Fatalf("range offender = %+v", got)
	}
	if got := guidance.FormatResolutionOffender(evidence.Resolution{Handle: "read#1", Line: 2, EndLine: 9}, guidance.WorkerFindingInput{}); got != "read#1:2-9" {
		t.Fatalf("handle offender = %q", got)
	}
}

// AC-AST-20: a source link is a workspace citation spelled as the ledger spells
// its root, with coordinates from the destination.
func TestDraftSourceLinks(t *testing.T) {
	t.Parallel()
	roots := multiRootFixture(t)
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "@shared/lib/sec.go", LineRanges: []evidence.LineRange{{Start: 1, End: 20}}},
		{Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "top.go", LineRanges: []evidence.LineRange{{Start: 1, End: 20}}},
	})
	cases := []struct {
		name, source string
		want         guidance.CitationOutcome
		path         string
		line, end    int
	}{
		{"secondary root", "[sec](source://s/lib/sec.go#L12-L14)", guidance.CitationGrounded, "@shared/lib/sec.go", 12, 14},
		{"range past the observation", "[sec](source://s/lib/sec.go#L12-L40)", guidance.CitationUnverifiable, "@shared/lib/sec.go", 12, 40},
		{"primary root", "[top](source://p/top.go#L3)", guidance.CitationGrounded, "top.go", 3, 0},
		{"wrong root", "[sec](source://p/lib/sec.go#L12)", guidance.CitationNotObserved, "", 12, 0},
		{"unknown root", "[sec](source://nope/lib/sec.go#L12)", guidance.CitationNotObserved, "", 12, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, roots, ev)
			if got.Kind != citationmd.KindWorkspaceLink || got.Outcome != tc.want || got.Resolution.Line != tc.line {
				t.Fatalf("outcome = %+v", got)
			}
			if tc.path != "" && (got.Resolution.Path != tc.path || got.Resolution.EndLine != tc.end) {
				t.Fatalf("resolution = %+v", got.Resolution)
			}
		})
	}
	single := ledgerOf(readMessages("a.go", lineTexts(1, 5)))
	if got := onlyOutcome(t, "[a](source://any/a.go#L2)", evidence.CitationRoots{}, single); got.Outcome != guidance.CitationGrounded {
		t.Fatalf("single root = %+v", got)
	}
	draft := guidance.ParseCloseoutDraft("[sec](source://s/lib/sec.go#L12)", guidance.DraftParseOptions(roots, ev))
	if len(draft.CitedEvidence) != 1 || draft.CitedEvidence[0].Path != "@shared/lib/sec.go" {
		t.Fatalf("metadata = %+v", draft.CitedEvidence)
	}
}

// M7: an empty ledger means nothing was observed, so a caller must have failed on a
// read error first. The union reports that error rather than an empty result.
func TestLedgerReadErrorSurfacesBeforeResolution(t *testing.T) {
	t.Parallel()
	want := errors.New("ledger unavailable")
	_, err := guidance.UnionCloseoutEvidence(context.Background(), failingEvidenceReader{err: want}, "session", nil)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	got := onlyOutcome(t, "[read#1]", evidence.CitationRoots{}, evidence.Ledger{})
	if got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("empty ledger = %+v", got)
	}
}

type failingEvidenceReader struct{ err error }

func (r failingEvidenceReader) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, r.err
}

func (r failingEvidenceReader) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, r.err
}

// H2, H3, H8: a coordinate needs line facts for that location, on any record.
func TestDraftCoordinatesAreChecked(t *testing.T) {
	t.Parallel()
	read := func(handle, path string, start, end int) evidence.Record {
		return evidence.Record{Handle: handle, Kind: "read", Shape: evidence.ShapeFileRegion, Path: path, LineRanges: []evidence.LineRange{{Start: start, End: end}}}
	}
	reads := evidence.AssembleLedger([]evidence.Record{read("read#1", "a.go", 1, 10)})
	survey := evidence.AssembleLedger([]evidence.Record{{Handle: "list#1", Kind: "list", Shape: evidence.ShapeFileRegion, Path: "pkg/doc.go", Survey: true}})
	both := evidence.AssembleLedger([]evidence.Record{{Handle: "list#1", Kind: "list", Shape: evidence.ShapeFileRegion, Path: "a.go", Survey: true}, read("read#1", "a.go", 1, 10)})
	gate := evidence.AssembleLedger([]evidence.Record{{Handle: "verify#1", Kind: "verify", GateType: "verify", Path: "g/h.go"}})
	zero := evidence.AssembleLedger([]evidence.Record{{Handle: "grep#1", Kind: "grep", Shape: evidence.ShapeOpaque, Path: "a.go"}})
	pathless := evidence.AssembleLedger([]evidence.Record{{Handle: "command#4", Kind: "command", Shape: evidence.ShapeCommand}, {Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque}})
	cases := []struct {
		name, source string
		ev           evidence.Ledger
		want         guidance.CitationOutcome
		handle       string
	}{
		{"AC-AST-01 single line past the read", "[a.go:999]", reads, guidance.CitationUnverifiable, "read#1"},
		{"explicit handle past the read", "[read#1:999]", reads, guidance.CitationUnverifiable, "read#1"},
		{"handle and path past the read", "[read#1 a.go:999]", reads, guidance.CitationUnverifiable, "read#1"},
		{"one-line range past the read", "[a.go:99-99]", reads, guidance.CitationUnverifiable, "read#1"},
		{"line just past the read", "[a.go:11]", reads, guidance.CitationUnverifiable, "read#1"},
		{"workspace link past the read", "[x](a.go#L999)", reads, guidance.CitationUnverifiable, "read#1"},
		{"line inside the read", "[a.go:10]", reads, guidance.CitationGrounded, "read#1"},
		{"one-line range inside the read", "[a.go:4-4]", reads, guidance.CitationGrounded, "read#1"},
		{"AC-AST-13 survey range", "[pkg/doc.go:1-50]", survey, guidance.CitationUnverifiable, "list#1"},
		{"survey line", "[pkg/doc.go:7]", survey, guidance.CitationUnverifiable, "list#1"},
		{"survey handle range", "[list#1:1-50]", survey, guidance.CitationUnverifiable, "list#1"},
		{"survey bare", "[pkg/doc.go]", survey, guidance.CitationSurvey, ""},
		{"AC-AST-01 survey cannot exempt a range", "[a.go:3-40]", both, guidance.CitationUnverifiable, "list#1"},
		{"survey and read bare binds content", "[a.go]", both, guidance.CitationGrounded, "read#1"},
		{"survey and read line binds content", "[a.go:3]", both, guidance.CitationGrounded, "read#1"},
		{"AC-AST-13 gate line", "[g/h.go:3]", gate, guidance.CitationUnverifiable, "verify#1"},
		{"gate excerpt", `[g/h.go:3 "some meaningful excerpt"]`, gate, guidance.CitationUnverifiable, "verify#1"},
		{"gate handle line", "[verify#1:3]", gate, guidance.CitationUnverifiable, "verify#1"},
		{"gate bare handle", "[verify#1]", gate, guidance.CitationGrounded, "verify#1"},
		{"AC-AST-11 zero match line", "[grep#1:3]", zero, guidance.CitationUnverifiable, "grep#1"},
		{"zero match path line", "[a.go:3]", zero, guidance.CitationUnverifiable, "grep#1"},
		{"zero match bare", "[grep#1]", zero, guidance.CitationGrounded, "grep#1"},
		{"zero match bare path", "[a.go]", zero, guidance.CitationGrounded, "grep#1"},
		{"command line", "[command#4:3]", pathless, guidance.CitationUnverifiable, "command#4"},
		{"dispatch line", "[task#1:1]", pathless, guidance.CitationUnverifiable, "task#1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want || got.Resolution.Handle != tc.handle {
				t.Fatalf("outcome = %+v", got)
			}
			if tc.want == guidance.CitationUnverifiable && got.Code != guidance.SynthCitationUnverifiableCode {
				t.Fatalf("code = %q", got.Code)
			}
		})
	}
}

// C2: this exact line once spun forever in excerpt anchoring. Resolving through
// JSON, which the grammar's bound does not cover, shows the loop cannot wrap.
func TestLineNearIntegerLimitTerminates(t *testing.T) {
	t.Parallel()
	ev := ledgerOf(readMessages("a.go", lineTexts(1, 10)))
	report := guidance.CoordinatorCompletionReport{Synthesis: "x", CitedEvidence: []guidance.CoordinatorCitedEvidence{
		{Evidence: "read#1", Line: math.MaxInt - 2, Excerpt: "content of line number 3"},
	}}
	guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, guidance.CloseoutEvidence{Ledger: ev})
}

// AC-AST-03: a leg may be named like a directive.
func TestDraftReservedNamespaces(t *testing.T) {
	t.Parallel()
	ev := evidence.NamespaceLedger(ledgerOf(readMessages("a.go", lineTexts(1, 9))), "verify")
	got := onlyOutcome(t, "[verify:read#1:2]", evidence.CitationRoots{}, ev)
	if got.Outcome != guidance.CitationGrounded || got.Resolution.Handle != "verify:read#1" {
		t.Fatalf("outcome = %+v", got)
	}
}

// The evidence catalog decides handle kinds, and the ledger adds any kind it holds.
func TestDraftHandleKinds(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{{Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque}})
	known := guidance.KnownEvidenceKind(ev)
	for kind, want := range map[string]bool{"read": true, "command": true, "task": true, "pr": false, "section": false} {
		if known(kind) != want {
			t.Errorf("known(%q) = %v", kind, !want)
		}
	}
	namespaced := evidence.NamespaceLedger(evidence.AssembleLedger([]evidence.Record{{Handle: "custom#1", Shape: evidence.ShapeOpaque}}), "leg")
	if !guidance.KnownEvidenceKind(namespaced)("custom") {
		t.Fatal("a namespaced handle's kind is known")
	}
	if got := resolveMarkdown("[pr#123] and [section#intro] and [read#3]", evidence.CitationRoots{}, ev); len(got.Occurrences) != 1 || got.Occurrences[0].Outcome != guidance.CitationNotObserved {
		t.Fatalf("resolution = %+v", got)
	}
}

// M2: a schemeless www link names neither scheme.
func TestDraftWWWMatchesEitherScheme(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, URL: "https://www.a.test/p"},
		{Handle: "web#2", Kind: "web", Shape: evidence.ShapeURL, URL: "http://www.b.test/p"},
	})
	for source, want := range map[string]guidance.CitationOutcome{
		"www.a.test/p":                         guidance.CitationGrounded,
		"www.b.test/p":                         guidance.CitationGrounded,
		"[www.a.test/p]":                       guidance.CitationGrounded,
		"www.c.test/p":                         guidance.CitationNotObserved,
		"[x](http://www.a.test/p)":             guidance.CitationNotObserved,
		`<a href="https://www.a.test/p">x</a>`: guidance.CitationGrounded,
		`<a href="https://www.z.test/p">x</a>`: guidance.CitationNotObserved,
	} {
		if got := onlyOutcome(t, source, evidence.CitationRoots{}, ev); got.Outcome != want {
			t.Errorf("%s = %+v", source, got)
		}
	}
}

// M10, low: a root with no label binds by identity; an id that names no root
// resolves nowhere, even when it equals another root's label.
func TestDraftSourceLinkRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	roots := evidence.CitationRoots{ActiveRootID: "r", Roots: []projectroot.RootRef{{ID: "r", Path: dir, IsPrimary: true}, {ID: "s", Label: "shared", Path: t.TempDir()}}}
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
		{Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "@shared/b.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
	})
	if got := onlyOutcome(t, "[x](source://r/a.go#L1)", roots, ev); got.Outcome != guidance.CitationGrounded {
		t.Fatalf("unlabeled root = %+v", got)
	}
	if got := onlyOutcome(t, "[x](source://shared/b.go#L1)", roots, ev); got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("a label is no root id: %+v", got)
	}
}

// M3: an unobserved raw anchor loses its tag at presentation.
func TestPresentRawAnchors(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, URL: "https://seen.test/p"}})
	roots := evidence.CitationRoots{}
	render := func(source string) string {
		draft := guidance.ParseCloseoutDraft(source, guidance.DraftParseOptions(roots, ev))
		return guidance.PresentCloseoutMarkdown(draft, guidance.ResolveDraftCitations(roots, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, nil))
	}
	if got := render(`a <a href="https://seen.test/p">b</a> c`); got != `a <a href="https://seen.test/p">b</a> c` {
		t.Fatalf("observed = %q", got)
	}
	if got := render(`a <a href="https://other.test/p">b</a> c`); strings.Contains(got, "other.test") || !strings.Contains(got, "b") {
		t.Fatalf("unobserved = %q", got)
	}
}
