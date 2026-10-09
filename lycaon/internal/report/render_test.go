package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// loadReportInputFixture mirrors reporttest.LoadReportInputFixture for tests
// inside package report, which cannot import reporttest (import cycle in the
// test binary). Relative path is fine: go test runs in the package directory.
func loadReportInputFixture(t *testing.T, name string) ReportInput {
	t.Helper()
	path := filepath.Join("testdata", "report_input", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var input ReportInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return input
}

func TestRender_Deterministic(t *testing.T) {
	input := loadReportInputFixture(t, "security_survey.json")
	a, err := Render(input)
	testutil.FailErr(t, "render a", err)
	b, err := Render(input)
	testutil.FailErr(t, "render b", err)
	if !bytes.Equal(a, b) {
		t.Fatalf("Render(same input) bytes differ (%d vs %d)", len(a), len(b))
	}
}

// A section is present iff its data is, so the layout is the assertion.
func TestRender_SectionsByDataPresence(t *testing.T) {
	full := loadReportInputFixture(t, "security_survey.json")
	front, body := buildBlocks(testMeasurer(t), full)
	joined := joinRowValues(append(front, body...))
	for _, want := range []string{
		sectionSummary, sectionFindings, sectionAssessment,
		sectionAdjudication, sectionScan, sectionEvidence, sectionVisuals, sectionSources, sectionColophon,
		sectionLimits,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("full layout missing section %q in %q", want, joined)
		}
	}
	if !strings.Contains(joined, seeAppendixPointer) {
		t.Fatalf("want dedup pointer %q in layout; got %q", seeAppendixPointer, joined)
	}
	if !strings.Contains(joined, "Login form after submit") {
		t.Fatalf("want capture caption in evidence appendix; got %q", joined)
	}
	if !strings.Contains(joined, "Signup — empty state") {
		t.Fatalf("want render caption in Visuals; got %q", joined)
	}

	coreOnly := loadReportInputFixture(t, "synthesis_only.json")
	front, body = buildBlocks(testMeasurer(t), coreOnly)
	joined = joinRowValues(append(front, body...))
	for _, banned := range []string{
		sectionAdjudication, sectionScan, sectionEvidence, sectionVisuals, sectionSources,
		sectionLimits,
	} {
		if strings.Contains(joined, banned) {
			t.Fatalf("synthesis_only layout must omit %q; got %q", banned, joined)
		}
	}
	// The colophon is the one host section every report carries: a document
	// with no record of what produced it cannot be checked.
	if !strings.Contains(joined, sectionColophon) {
		t.Fatalf("synthesis_only layout must still carry %q; got %q", sectionColophon, joined)
	}
}

// The document opens with the brief, which ends its page, and the working
// summary follows with the closeout's own headline.
func TestRender_BriefThenWorkingSummary(t *testing.T) {
	ms := testMeasurer(t)
	in := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "Security survey",
			Headline:    "The service is not safe to expose without two fixes.",
			RunID:       "run_1",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
		}, Synthesis: "ok",
	}
	front, _ := buildBlocks(ms, in)
	if len(front) < 2 || !front[0].breakAfter {
		t.Fatalf("front = %d blocks; want the brief first, ending its page", len(front))
	}
	brief := joinRowValues(front[:1])
	if !strings.Contains(brief, "The check is complete.") || strings.Contains(brief, in.Headline) {
		t.Fatalf("brief = %q; want the host's answer line, not the authored headline", brief)
	}
	if rest := joinRowValues(front[1:]); !strings.Contains(rest, in.Headline) {
		t.Fatalf("working summary = %q; want the authored headline", rest)
	}
}

func TestRender_MissingArtifactBytesShrinks(t *testing.T) {
	input := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "No bytes",
			RunID:       "run_nobbytes",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
			HeadSHA:     "abc",
		},
		Synthesis: "## Summary\n\nok",
		Artifacts: []ReportArtifact{
			{ID: "gone", Caption: "Evicted mockup", Mime: "image/png"},
			{ID: "gone", Caption: "Repeat cite", Mime: "image/png"},
		},
	}
	front, body := buildBlocks(testMeasurer(t), input)
	joined := joinRowValues(append(front, body...))
	if strings.Contains(joined, sectionVisuals) || strings.Contains(joined, "Evicted") {
		t.Fatalf("want no visual stubs; got %q", joined)
	}
	pdf, err := Render(input)
	testutil.FailErr(t, "render", err)
	if len(pdf) < 100 {
		t.Fatalf("pdf too small: %d", len(pdf))
	}
}

func TestRender_ScanRowsInSeverityOrder(t *testing.T) {
	input := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "Sort",
			RunID:       "run_sort",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
			HeadSHA:     "abc",
		},
		Synthesis: "## Summary\n\nok",
		ScanRows: []ReportScanRow{
			{Severity: "low", RuleID: "z", File: "a.go", Line: 1, Message: "lowest message"},
			{Severity: "high", RuleID: "a", File: "b.go", Line: 2, Message: "highest message"},
			{Severity: "medium", RuleID: "m", File: "c.go", Line: 3, Message: "middle message"},
		},
	}
	_, body := buildBlocks(testMeasurer(t), input)
	joined := joinRowValues(body)
	hi := strings.Index(joined, "highest message")
	med := strings.Index(joined, "middle message")
	lo := strings.Index(joined, "lowest message")
	if hi < 0 || med < 0 || lo < 0 {
		t.Fatalf("missing finding messages in %q", joined)
	}
	if hi >= med || med >= lo {
		t.Fatalf("severity order want high < medium < low; positions %d %d %d in %q", hi, med, lo, joined)
	}
}

func TestRender_LongFindingsNoPanic(t *testing.T) {
	findings := make([]ReportScanRow, 80)
	for i := range findings {
		findings[i] = ReportScanRow{
			Severity: "medium",
			RuleID:   "rule.example." + string(rune('a'+i%7)),
			File:     "pkg/file.go",
			Line:     i + 1,
			Message:  "finding spills across pages; row must stay intact",
		}
	}
	input := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "Long",
			RunID:       "run_long",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
			HeadSHA:     "abc",
		},
		Synthesis: "## Summary\n\nmany findings",
		ScanRows:  findings,
	}
	pdf, err := Render(input)
	testutil.FailErr(t, "render long findings", err)
	if len(pdf) < 1000 {
		t.Fatalf("pdf unexpectedly small: %d", len(pdf))
	}
}

func TestRender_GoldenFixtures(t *testing.T) {
	cases := reportInputFixtures(t)
	dir := goldenPDFDir()
	update := os.Getenv("UPDATE_REPORT_GOLDEN") == "1"

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			input := loadReportInputFixture(t, name)
			got, err := Render(input)
			testutil.FailErr(t, "render", err)

			goldenName := strings.TrimSuffix(name, ".json") + ".pdf"
			path := filepath.Join(dir, goldenName)
			if update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					testutil.FailErr(t, "mkdir golden", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					testutil.FailErr(t, "write golden", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden %s (UPDATE_REPORT_GOLDEN=1 to write it): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("golden mismatch for %s: got %d bytes want %d (UPDATE_REPORT_GOLDEN=1 if intentional)", goldenName, len(got), len(want))
			}
			again, err := Render(input)
			testutil.FailErr(t, "re-render", err)
			if !bytes.Equal(again, want) {
				t.Fatal("re-render not byte-identical to golden")
			}
		})
	}
}

func goldenPDFDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "golden")
}

// reportInputFixtures enumerates every fixture in testdata/report_input; each
// one's layout is held against a golden.
func reportInputFixtures(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join("testdata", "report_input", "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no report_input fixtures found: %v", err)
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Base(n))
	}
	sort.Strings(out)
	return out
}
