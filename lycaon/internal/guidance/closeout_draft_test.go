package guidance_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	draft := guidance.ParseCloseoutDraft(source, citationmd.Options{KnownPath: guidance.KnownLedgerPath(roots, ev)})
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

func TestDraftRangeValidation(t *testing.T) {
	t.Parallel()
	contiguous := ledgerOf(readMessages("a.go", lineTexts(42, 45)))
	gapped := ledgerOf(readMessages("a.go", lineTexts(10, 12), lineTexts(20, 22)))
	abutting := ledgerOf(readMessages("a.go", lineTexts(10, 12), lineTexts(13, 15)))
	twoReads := ledgerOf(readMessages("a.go", lineTexts(1, 5)), readMessages("a.go", lineTexts(100, 110)))
	cases := []struct {
		name     string
		source   string
		ev       evidence.Ledger
		want     guidance.CitationOutcome
		wantLine int
		wantEnd  int
	}{
		{"AC-AST-01 unsupported range", "[read#1:42-58]", contiguous, guidance.CitationUnverifiable, 42, 58},
		{"AC-AST-01 unsupported path range", "[a.go:42-58]", contiguous, guidance.CitationUnverifiable, 42, 58},
		{"covered handle range", "[read#1:42-45]", contiguous, guidance.CitationGrounded, 42, 45},
		{"covered path range", "[a.go:43-44]", contiguous, guidance.CitationGrounded, 43, 44},
		{"range starts before observation", "[read#1:41-45]", contiguous, guidance.CitationUnverifiable, 41, 45},
		{"AC-AST-02 range across a gap", "[read#1:11-21]", gapped, guidance.CitationUnverifiable, 11, 21},
		{"both endpoints observed but gap", "[a.go:12-20]", gapped, guidance.CitationUnverifiable, 12, 20},
		{"one interval of several", "[read#1:20-22]", gapped, guidance.CitationGrounded, 20, 22},
		{"abutting intervals cover", "[read#1:11-14]", abutting, guidance.CitationGrounded, 11, 14},
		{"path range lands on the record that covers it", "[a.go:101-105]", twoReads, guidance.CitationGrounded, 101, 105},
		{"explicit handle keeps its own record", "[read#1:101-105]", twoReads, guidance.CitationUnverifiable, 101, 105},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %+v, want %s", got, tc.want)
			}
			if got.Resolution.Line != tc.wantLine || got.Resolution.EndLine != tc.wantEnd {
				t.Fatalf("range = %d-%d, want %d-%d", got.Resolution.Line, got.Resolution.EndLine, tc.wantLine, tc.wantEnd)
			}
			if tc.want == guidance.CitationUnverifiable {
				if got.Code != guidance.SynthCitationUnverifiableCode {
					t.Fatalf("code = %q", got.Code)
				}
				if want := fmt.Sprintf(":%d-%d", tc.wantLine, tc.wantEnd); !strings.Contains(got.Offender, want) {
					t.Fatalf("offender %q lacks the range %q", got.Offender, want)
				}
			}
		})
	}
}

// AC-AST-13: an excerpt shifts both ends and the whole range is checked again.
func TestDraftRangeExcerptAnchoring(t *testing.T) {
	t.Parallel()
	long := ledgerOf(readMessages("a.go", lineTexts(40, 80)))
	repeated := ledgerOf(readMessages("a.go", spanBlock{start: 40, lines: []string{
		"needle in the file", "x", "y", "z", "w", "v", "needle in the file",
	}}))
	gap := ledgerOf(readMessages("a.go", lineTexts(10, 12), lineTexts(14, 20)))
	cases := []struct {
		name          string
		source        string
		ev            evidence.Ledger
		want          guidance.CitationOutcome
		line, end     int
		correctedFrom int
	}{
		{"excerpt inside the range moves nothing", `[read#1:42-50 "content of line number 47"]`, long, guidance.CitationGrounded, 42, 50, 0},
		{"excerpt at the start", `[a.go:42-50 "content of line number 42"]`, long, guidance.CitationGrounded, 42, 50, 0},
		{"drift below the start shifts both ends", `[read#1:42-50 "content of line number 40"]`, long, guidance.CitationGrounded, 40, 48, 42},
		{"drift past the end shifts both ends", `[read#1:42-50 "content of line number 52"]`, long, guidance.CitationGrounded, 44, 52, 42},
		{"far unique path anchor shifts both ends", `[a.go:42-44 "content of line number 70"]`, long, guidance.CitationGrounded, 70, 72, 42},
		{"far anchor past the observation refuses", `[a.go:42-44 "content of line number 80"]`, long, guidance.CitationUnverifiable, 42, 44, 0},
		{"ambiguous excerpt is no match", `[read#1:42-44 "needle in the file"]`, repeated, guidance.CitationUnverifiable, 42, 44, 0},
		{"shift onto a gap refuses", `[read#1:10-12 "content of line number 14"]`, gap, guidance.CitationUnverifiable, 10, 12, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %+v, want %s", got, tc.want)
			}
			res := got.Resolution
			if res.Line != tc.line || res.EndLine != tc.end || res.LineCorrectedFrom != tc.correctedFrom {
				t.Fatalf("resolution = %+v, want %d-%d from %d", res, tc.line, tc.end, tc.correctedFrom)
			}
		})
	}
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
	wrongPath := onlyOutcome(t, `[other.ts:3 "func NewBoard() {"]`, evidence.CitationRoots{}, unique)
	if wrongPath.Outcome != guidance.CitationNotObserved || wrongPath.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("unobserved path = %+v", wrongPath)
	}
	if got := onlyOutcome(t, "[other/file.ts]", evidence.CitationRoots{}, unique); got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("unobserved bare path = %+v", got)
	}
	superseded := ledgerOf(readMessages("a.go", lineTexts(1, 5)), readMessages("a.go", lineTexts(1, 9)))
	if rec, _ := evidence.ResolveHandle(superseded, "read#1"); rec.SupersededBy == "" {
		t.Fatalf("fixture does not supersede: %+v", rec)
	}
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
	retired := evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "old.go", SupersededBy: "edit#1"}})
	if got := onlyOutcome(t, "[old/../old.go]", evidence.CitationRoots{}, retired); got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("traversal = %+v", got)
	}
	if got := onlyOutcome(t, "[./old.go]", evidence.CitationRoots{}, retired); got.Outcome != guidance.CitationUnverifiable {
		t.Fatalf("only a superseded record = %+v", got)
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
