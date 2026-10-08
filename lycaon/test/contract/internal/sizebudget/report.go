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
// scripts/budgets.py.
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

// ReportedFinding is a finding with the files its artifact is made from.
type ReportedFinding struct {
	Finding
	Sources []string `json:"sources"`
}

// Report is the machine-readable result of one suite.
type Report struct {
	Suite      string                    `json:"suite"`
	Policy     string                    `json:"policy"`
	Categories map[string]reportCategory `json:"categories"`
	Findings   []ReportedFinding         `json:"findings"`
	// Untouched counts, per category, artifacts past their limit that the
	// change did not touch and no exception admits.
	Untouched map[string]int `json:"untouched"`
	// Warnings and Notes carry suite-wide observations, such as model-window headroom.
	Warnings []string `json:"warnings"`
	Notes    []string `json:"notes"`
}

type reportCategory struct {
	Category
	Limit
}

// NewReport assembles a suite report. sources maps category → artifact ID →
// the repository-relative files or directories the artifact is made from.
func NewReport(s Suite, p Policy, findings []Finding, untouched map[string]int, sources map[string]map[string][]string) Report {
	report := Report{Suite: s.Name, Policy: s.PolicyPath, Categories: map[string]reportCategory{},
		Findings: []ReportedFinding{}, Untouched: untouched, Warnings: []string{}, Notes: []string{}}
	for name, c := range s.Categories {
		report.Categories[name] = reportCategory{c, p.Limits[name]}
	}
	for _, f := range findings {
		files := sources[f.Category][f.ID]
		if files == nil {
			files = []string{}
		}
		report.Findings = append(report.Findings, ReportedFinding{f, files})
	}
	if report.Untouched == nil {
		report.Untouched = map[string]int{}
	}
	return report
}

// WriteReport writes the report when scripts/budgets.py asked for one.
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

// FailureReport explains each failing finding and how to answer it. detail
// adds suite-specific lines, such as the files behind an artifact.
func FailureReport(s Suite, failures []Finding, detail func(Finding) []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s budget: %d artifact(s) past a chosen line\npolicy: %s\n", s.Name, len(failures), s.PolicyPath)
	for _, f := range failures {
		c := s.Categories[f.Category]
		bound := "limit"
		if f.Kind == OverCap {
			bound = "exception cap"
		}
		fmt.Fprintf(&out, "\n%s: %s[%q]\nmeasures: %s\nmeasured: %d %s | %s: %d %s | over by %d\n",
			f.Kind, f.Category, f.ID, c.Measures, f.Measured, c.Unit, bound, f.Bound, c.Unit, f.Measured-f.Bound)
		if detail != nil {
			for _, line := range detail(f) {
				out.WriteString(line + "\n")
			}
		}
		fmt.Fprintf(&out, "remedy: %s\n", c.Remedy)
		if f.Kind == OverCap {
			fmt.Fprintf(&out, "exception reason: %s\nfix: make it smaller, or revisit the reason and its cap.\n", f.Reason)
		} else {
			out.WriteString("fix: bring it within its limit. If it must be this large, add an exception with a reason a reviewer can weigh.\n")
		}
	}
	return out.String()
}
