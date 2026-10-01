// Package reporttest validates report inputs and loads the shared fixtures for
// tests in other packages. It is imported only from _test.go files so it stays
// out of every shipped binary's import graph; tests inside package report use
// the local loader in render_test.go instead (import cycle).
package reporttest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/report"
)

// AssertReportInput validates required core fields and well-formed optional
// sections. A core-only report carries none of the optional ones.
func AssertReportInput(t testing.TB, input report.ReportInput) {
	t.Helper()
	requireNonEmpty(t, "title", input.Title)
	requireNonEmpty(t, "run_id", input.RunID)
	requireNonEmpty(t, "project", input.Project)
	requireNonEmpty(t, "completed_at", input.CompletedAt)
	if _, err := time.Parse(time.RFC3339, input.CompletedAt); err != nil {
		t.Fatalf("completed_at %q: want RFC3339: %v", input.CompletedAt, err)
	}
	requireNonEmpty(t, "synthesis", input.Synthesis)

	if input.StartedAt != "" {
		if _, err := time.Parse(time.RFC3339, input.StartedAt); err != nil {
			t.Fatalf("started_at %q: want RFC3339: %v", input.StartedAt, err)
		}
	}
	for i, v := range input.Verdicts {
		prefix := fmt.Sprintf("verdicts[%d]", i)
		requireNonEmpty(t, prefix+".decision", v.Decision)
		if v.RecordedAt != "" {
			if _, err := time.Parse(time.RFC3339, v.RecordedAt); err != nil {
				t.Fatalf("%s.recorded_at %q: want RFC3339: %v", prefix, v.RecordedAt, err)
			}
		}
		for j, f := range v.Fields {
			field := fmt.Sprintf("%s.fields[%d]", prefix, j)
			requireNonEmpty(t, field+".name", f.Name)
			requireNonEmpty(t, field+".value", f.Value)
			// The reserved citation channels are not schema members; projecting
			// one as a field prints a raw record where a value belongs.
			if f.Name == "cited_evidence" || f.Name == "cited_urls" {
				t.Fatalf("%s.name = %q: a reserved citation channel is not a verdict field", field, f.Name)
			}
		}
		for j, c := range v.Claims {
			claim := fmt.Sprintf("%s.claims[%d]", prefix, j)
			requireNonEmpty(t, claim+".id", c.ID)
			requireNonEmpty(t, claim+".statement", c.Statement)
			for k, cite := range c.CitedEvidence {
				if strings.TrimSpace(cite.Handle) == "" && strings.TrimSpace(cite.Path) == "" {
					t.Fatalf("%s.cited_evidence[%d]: want a handle or a path", claim, k)
				}
			}
		}
	}
	if s := input.Scan; s != nil {
		if s.Total < 0 || s.Listed < 0 || s.Listed > s.Total {
			t.Fatalf("scan: listed=%d total=%d: want 0 <= listed <= total", s.Listed, s.Total)
		}
		if s.Stored > 0 && s.Listed > s.Stored {
			t.Fatalf("scan: listed=%d exceeds stored=%d", s.Listed, s.Stored)
		}
		// Stored plus merged cannot exceed the total; ignored rows are part of stored.
		if s.Stored+s.Merged > s.Total {
			t.Fatalf("scan: stored=%d + merged=%d exceeds total=%d", s.Stored, s.Merged, s.Total)
		}
		if s.Ignored > s.Stored {
			t.Fatalf("scan: ignored=%d exceeds stored=%d", s.Ignored, s.Stored)
		}
	}
	for i, f := range input.Findings {
		prefix := fmt.Sprintf("findings[%d]", i)
		// A finding without a title states nothing a reader can act on.
		requireNonEmpty(t, prefix+".title", f.Title)
		for j, w := range f.Where {
			if strings.TrimSpace(w.Handle) == "" && strings.TrimSpace(w.Path) == "" {
				t.Fatalf("%s.where[%d]: want a handle or a path", prefix, j)
			}
		}
	}
	for i, r := range input.ScanRules {
		requireNonEmpty(t, fmt.Sprintf("scan_rules[%d].id", i), r.ID)
	}
	for i, f := range input.ScanRows {
		prefix := fmt.Sprintf("scan_rows[%d]", i)
		requireNonEmpty(t, prefix+".severity", f.Severity)
		requireNonEmpty(t, prefix+".rule_id", f.RuleID)
		requireNonEmpty(t, prefix+".file", f.File)
		requireNonEmpty(t, prefix+".message", f.Message)
		if f.Line < 0 {
			t.Fatalf("%s.line = %d: want >= 0", prefix, f.Line)
		}
	}
	for i, e := range input.Evidence {
		prefix := fmt.Sprintf("evidence[%d]", i)
		requireNonEmpty(t, prefix+".handle", e.Handle)
		requireNonEmpty(t, prefix+".kind", e.Kind)
		requireNonEmpty(t, prefix+".trust_tier", e.TrustTier)
		if e.Line < 0 {
			t.Fatalf("%s.line = %d: want >= 0", prefix, e.Line)
		}
		if e.LineEnd != 0 && e.LineEnd < e.Line {
			t.Fatalf("%s: line_end=%d before line=%d", prefix, e.LineEnd, e.Line)
		}
		for j, by := range e.CitedBy {
			requireNonEmpty(t, fmt.Sprintf("%s.cited_by[%d]", prefix, j), by)
		}
	}
	if input.EvidenceTotal != 0 && input.EvidenceTotal < len(input.Evidence) {
		t.Fatalf("evidence_total=%d is smaller than the %d records listed", input.EvidenceTotal, len(input.Evidence))
	}
	for i, s := range input.Sources {
		requireNonEmpty(t, fmt.Sprintf("sources[%d].url", i), s.URL)
	}
	for i, a := range input.Artifacts {
		prefix := fmt.Sprintf("artifacts[%d]", i)
		requireNonEmpty(t, prefix+".id", a.ID)
		requireNonEmpty(t, prefix+".caption", a.Caption)
	}
	assertBrief(t, input)
}

// assertBrief checks the first page's inputs: a rating whose indexes name
// declared levels, findings with known dispositions, and checks and gaps of
// known kinds.
func assertBrief(t testing.TB, input report.ReportInput) {
	t.Helper()
	if b := input.Brief; b != nil {
		requireNonEmpty(t, "brief.question", b.Question)
		n := len(b.Levels)
		if n < 2 || b.Worst < 0 || b.Best >= n || b.Worst > b.Best {
			t.Fatalf("brief: worst=%d best=%d over %d levels: want 0 <= worst <= best < levels", b.Worst, b.Best, n)
		}
		for i, r := range b.Rated {
			if r.Number < 1 || r.Number > len(input.Findings) {
				t.Fatalf("brief.rated[%d].number = %d: want a finding's position", i, r.Number)
			}
			if len(r.Answers) != len(b.Dimensions) {
				t.Fatalf("brief.rated[%d]: %d answers for %d dimensions", i, len(r.Answers), len(b.Dimensions))
			}
			if r.Worst < 0 || r.Best >= n || r.Worst > r.Best {
				t.Fatalf("brief.rated[%d]: worst=%d best=%d out of range", i, r.Worst, r.Best)
			}
		}
	}
	for i, f := range input.Findings {
		switch f.Disposition {
		case "", report.DispositionAct, report.DispositionAccept, report.DispositionHeld:
		default:
			t.Fatalf("findings[%d].disposition = %q", i, f.Disposition)
		}
	}
	for i, c := range input.Checks {
		switch c.Kind {
		case report.CheckArea, report.CheckReview, report.CheckScans:
		default:
			t.Fatalf("checks[%d].kind = %q", i, c.Kind)
		}
		switch c.State {
		case report.CheckDone, report.CheckPartial, report.CheckUnchecked:
		default:
			t.Fatalf("checks[%d].state = %q", i, c.State)
		}
	}
	for i, g := range input.Gaps {
		requireNonEmpty(t, fmt.Sprintf("gaps[%d].kind", i), g.Kind)
		if g.Of > 0 && g.Count > g.Of {
			t.Fatalf("gaps[%d]: count=%d exceeds of=%d", i, g.Count, g.Of)
		}
	}
}

// LoadReportInputFixture loads internal/report/testdata/report_input/<name>.
func LoadReportInputFixture(t testing.TB, name string) report.ReportInput {
	t.Helper()
	path := filepath.Join(reportInputTestdataDir(), name)
	// #nosec G304 -- path is a named report fixture.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var input report.ReportInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return input
}

func reportInputTestdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata", "report_input")
}

func requireNonEmpty(t testing.TB, field, value string) {
	t.Helper()
	if strings.TrimSpace(value) == "" {
		t.Fatalf("%s: want non-empty", field)
	}
}
