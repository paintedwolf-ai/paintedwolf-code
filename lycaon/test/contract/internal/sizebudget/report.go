package sizebudget

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ReportDirEnv names the directory where suites write their JSON report for
// the change report (scripts/budgets.py).
const ReportDirEnv = "PW_BUDGET_REPORT_DIR"

// Category describes what one category measures and how to answer growth.
type Category struct {
	Unit     string `json:"unit"`
	Measures string `json:"measures"`
	Remedy   string `json:"remedy"`
}

// Suite names one budget suite and where its policy lives.
type Suite struct {
	Name       string
	PolicyPath string // repository-relative
	Refresh    string // command that tightens grandfathered caps
	Categories map[string]Category
}

// CategoryNames lists the suite's categories in order.
func (s Suite) CategoryNames() []string {
	names := make([]string, 0, len(s.Categories))
	for name := range s.Categories {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Artifact is one measured artifact and the files it is made from.
type Artifact struct {
	Category string   `json:"category"`
	ID       string   `json:"id"`
	Measured int      `json:"measured"`
	Cap      int      `json:"cap"`
	Entry    Entry    `json:"entry,omitempty"`
	Sources  []string `json:"sources"`
}

// Report is the machine-readable result of one suite.
type Report struct {
	Suite      string                    `json:"suite"`
	Policy     string                    `json:"policy"`
	Refresh    string                    `json:"refresh"`
	Categories map[string]reportCategory `json:"categories"`
	Artifacts  []Artifact                `json:"artifacts"`
	Findings   []Finding                 `json:"findings"`
	// Notes carry suite-wide observations, such as model-window headroom.
	Notes []string `json:"notes"`
}

type reportCategory struct {
	Category
	Limit
}

// NewReport assembles a suite report. sources maps category → artifact ID →
// the repository-relative files or directories the artifact is made from.
func NewReport(s Suite, p Policy, measured Measurements, sources map[string]map[string][]string, findings []Finding, notes []string) Report {
	report := Report{Suite: s.Name, Policy: s.PolicyPath, Refresh: s.Refresh, Categories: map[string]reportCategory{}, Findings: findings, Notes: notes}
	if report.Findings == nil {
		report.Findings = []Finding{}
	}
	if report.Notes == nil {
		report.Notes = []string{}
	}
	for name, c := range s.Categories {
		report.Categories[name] = reportCategory{c, p.Limits[name]}
	}
	for _, name := range s.CategoryNames() {
		ids := make([]string, 0, len(measured[name]))
		for id := range measured[name] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			limitCap, entry := p.Cap(name, id)
			paths := sources[name][id]
			if paths == nil {
				paths = []string{}
			}
			report.Artifacts = append(report.Artifacts, Artifact{name, id, measured[name][id], limitCap, entry, paths})
		}
	}
	return report
}

// WriteReport writes the report when the change report asked for one.
func WriteReport(report Report) error {
	dir := os.Getenv(ReportDirEnv)
	if dir == "" {
		return nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode %s budget report: %w", report.Suite, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create budget report directory: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, report.Suite+".json"), raw, 0o644)
}

// Failures returns the findings that fail the suite.
func Failures(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Kind.Fails() {
			out = append(out, f)
		}
	}
	return out
}

// FailureReport explains each failing finding and how to answer it. detail
// adds suite-specific lines, such as the files behind an artifact.
func FailureReport(s Suite, failures []Finding, detail func(Finding) []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s budget: %d artifact(s) grew past a chosen line\npolicy: %s\n", s.Name, len(failures), s.PolicyPath)
	for _, f := range failures {
		c := s.Categories[f.Category]
		fmt.Fprintf(&out, "\n%s: %s[%q]\nmeasures: %s\nmeasured: %d %s | %s: %d %s | over by %d\n",
			f.Kind, f.Category, f.ID, c.Measures, f.Measured, c.Unit, boundName(f), f.Bound, c.Unit, f.Measured-f.Bound)
		if detail != nil {
			for _, line := range detail(f) {
				out.WriteString(line + "\n")
			}
		}
		fmt.Fprintf(&out, "remedy: %s\n", c.Remedy)
		switch {
		case f.Kind == GrandfatherRaised:
			out.WriteString("fix: grandfathered caps only shrink. Restore the base cap, or move the artifact to exceptions with a reason.\n")
		case f.Entry == EntryGrandfathered:
			out.WriteString("fix: this artifact predates its limit and may not grow. Make it smaller, or move it to exceptions with a reason a reviewer can weigh.\n")
		case f.Entry == EntryException:
			fmt.Fprintf(&out, "exception reason: %s\nfix: make it smaller, or raise the exception cap and update its reason.\n", f.Reason)
		default:
			out.WriteString("fix: make it smaller. If it must be this large, add an exception with a reason a reviewer can weigh.\n")
		}
	}
	return out.String()
}

func boundName(f Finding) string {
	switch {
	case f.Kind == GrandfatherRaised:
		return "base cap"
	case f.Entry != EntryNone:
		return "cap"
	default:
		return "limit"
	}
}
