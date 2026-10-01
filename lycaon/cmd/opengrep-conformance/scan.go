package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/runnerbudget"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

type scanReport struct {
	Parsed              *scanoutput.Result `json:"-"`
	CPUTimeMilliseconds int64              `json:"-"`
	OutputBytes         int64              `json:"-"`
	MemorySamples       int                `json:"-"`
	MemoryFailure       string             `json:"-"`
	PeakRSSBytes        int64              `json:"-"`
	Coverage            map[string][]string
	Paths               struct {
		Scanned []string `json:"scanned"`
	} `json:"paths"`
	Results []struct {
		RuleID string `json:"check_id"`
		Start  struct {
			Line int `json:"line"`
			Col  int `json:"col"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
			Col  int `json:"col"`
		} `json:"end"`
		Path string `json:"path"`
	} `json:"results"`
	Errors   []json.RawMessage `json:"errors"`
	Warnings []api.ScanWarning `json:"-"`
}

func scan(ctx context.Context, binary, root string, bundle []byte, analysis opengrep.Analysis) (*scanReport, error) {
	report, err := scanTargets(ctx, binary, root, bundle, []string{"source"}, "report.json", analysis)
	if err != nil {
		return report, err
	}
	origins, limitations, err := sourceview.Materialize(ctx, root, filepath.Join(root, "embedded"), report.Paths.Scanned)
	if err != nil {
		return report, err
	}
	for _, limitation := range limitations {
		report.Warnings = append(report.Warnings, api.ScanWarning{Kind: api.ScanWarningFilePartialParse, File: limitation.File, Construct: limitation.Construct, Message: limitation.Detail})
	}
	if len(origins) > 0 {
		var targets []string
		for path := range origins {
			targets = append(targets, path)
		}
		extra, err := scanTargets(ctx, binary, root, bundle, targets, "embedded.json", analysis)
		if extra != nil {
			report.OutputBytes += extra.OutputBytes
			report.CPUTimeMilliseconds += extra.CPUTimeMilliseconds
			report.PeakRSSBytes = max(report.PeakRSSBytes, extra.PeakRSSBytes)
			report.MemorySamples += extra.MemorySamples
			if extra.MemorySamples == 0 || extra.MemoryFailure != "" {
				report.MemoryFailure = "embedded scan memory measurement incomplete: " + extra.MemoryFailure
			}
		} else {
			report.MemoryFailure = "embedded scan did not start"
		}
		if err != nil {
			return report, err
		}
		if err := scanoutput.RemapOpengrepSources(extra.Parsed, origins, root); err != nil {
			return report, err
		}
		if err := scanoutput.ValidateOpengrepLocations(extra.Parsed, root); err != nil {
			return report, err
		}
		report.Warnings = append(report.Warnings, extra.Parsed.Warnings...)
		report.Parsed.Findings = append(report.Parsed.Findings, extra.Parsed.Findings...)
		for _, finding := range extra.Results {
			path := finding.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			origin, exists := origins[path]
			if !exists {
				return report, fmt.Errorf("unknown projected source %s", finding.Path)
			}
			start, end, ok := origin.Map.MapSpan(sourceview.Position{Line: finding.Start.Line, Column: finding.Start.Col}, sourceview.Position{Line: finding.End.Line, Column: finding.End.Col})
			if !ok {
				return report, fmt.Errorf("projected finding has no source span: %s", finding.Path)
			}
			finding.Path = origin.Path
			finding.Start.Line, finding.Start.Col = start.Line, start.Column
			finding.End.Line, finding.End.Col = end.Line, end.Column
			report.Results = append(report.Results, finding)
		}
	}
	coverage, err := rules.CoverageRuleIDs(bundle)
	if err != nil {
		return report, err
	}
	report.Coverage = make(map[string][]string)
	generic, err := rules.GenericRuleIDs(bundle)
	if err != nil {
		return report, err
	}
	sourceContexts := make(map[string]*sourceview.NonCode)
	filtered := report.Results[:0]
	for _, finding := range report.Results {
		if _, ok := coverage[finding.RuleID]; ok {
			report.Coverage[finding.Path] = append(report.Coverage[finding.Path], finding.RuleID)
			continue
		}
		if generic[finding.RuleID] {
			context := sourceContexts[finding.Path]
			if context == nil {
				context, err = sourceview.ReadNonCode(ctx, root, finding.Path)
				if err != nil {
					var limitation *sourceview.Limitation
					if !errors.As(err, &limitation) {
						return report, err
					}
					report.Warnings = append(report.Warnings, api.ScanWarning{Kind: api.ScanWarningFilePartialParse, File: finding.Path, Construct: limitation.Construct, Message: limitation.Detail})
					context = &sourceview.NonCode{}
				}
				sourceContexts[finding.Path] = context
			}
			if context.CoversSpan(finding.Start.Line, finding.Start.Col, finding.End.Line, finding.End.Col) {
				continue
			}
		}
		filtered = append(filtered, finding)
	}
	report.Results = filtered
	return report, nil
}

func scanTargets(ctx context.Context, binary, root string, bundle []byte, targets []string, reportName string, analysis opengrep.Analysis) (*scanReport, error) {
	config := filepath.Join(root, "rules.yaml")
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "rules.yaml"},
		Source:   bytes.NewReader(bundle), Mode: 0o600,
	}); err != nil {
		return nil, err
	}
	reportPath := filepath.Join(root, reportName)
	args, err := (opengrep.Invocation{Analysis: analysis, Output: reportPath, Rules: []string{config}, Targets: targets, Jobs: 1}).Args()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- scanner executable is an explicit developer flag; fixtures are arguments, never executed.
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "SEMGREP_SEND_METRICS=off", "SEMGREP_SETTINGS_FILE="+filepath.Join(root, "settings.yaml"), "SEMGREP_LOG_FILE="+filepath.Join(root, "scan.log"))
	var output diagnosticCapture
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = 10 * time.Second * time.Duration(runnerbudget.TimeoutScale())
	resources, runErr := runEvaluationProcess(cmd)
	runErr = errors.Join(runErr, ctx.Err())
	report := scanReport{PeakRSSBytes: resources.PeakRSSBytes, MemorySamples: resources.Samples, MemoryFailure: resources.Failure}
	if cmd.ProcessState != nil {
		report.CPUTimeMilliseconds = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Milliseconds()
	}
	raw, err := readEvaluationReport(reportPath)
	report.OutputBytes = int64(len(raw))
	if err != nil {
		return &report, fmt.Errorf("scan report: %w\n%s", errors.Join(err, runErr), &output)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return &report, err
	}

	parsed, err := scanoutput.ParseOpengrepJSON(bytes.NewReader(raw))
	if err != nil {
		return &report, fmt.Errorf("scanner report: %w\n%s", err, &output)
	}
	if err := scanoutput.ValidateOpengrepLocations(parsed, root); err != nil {
		return &report, err
	}
	scanoutput.NormalizeResultPaths(parsed, root)
	report.Parsed = parsed
	report.Warnings = parsed.Warnings
	var exit *exec.ExitError
	code := 0
	if runErr != nil {
		if !errors.As(runErr, &exit) {
			return &report, fmt.Errorf("scan: %w\n%s", runErr, &output)
		}
		code = exit.ExitCode()
	}
	if err := opengrep.ValidateReportExit(code, parsed.FindingsCount, len(parsed.Warnings)); err != nil {
		return &report, err
	}
	return &report, nil
}

func compare(report *scanReport, cases map[string]sourceCase) error {
	if err := compareCaseDiagnostics(report.Warnings, cases); err != nil {
		return err
	}
	got := make(map[string][]string)
	for _, finding := range report.Results {
		if _, ok := cases[finding.Path]; !ok {
			return fmt.Errorf("unexpected scanned path %s", finding.Path)
		}
		got[finding.Path] = append(got[finding.Path], finding.RuleID)
	}
	var failures []string
	paths := make([]string, 0, len(cases))
	for path := range cases {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		if err := compareCaseEvidence(report, path, cases[path].Evidence); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if !slices.Contains(report.Paths.Scanned, path) {
			failures = append(failures, fmt.Sprintf("%s: engine did not scan the case", path))
			continue
		}
		gap := slices.Clone(report.Coverage[path])
		slices.Sort(gap)
		expectedGap := slices.Clone(cases[path].Coverage)
		slices.Sort(expectedGap)
		if !slices.Equal(gap, expectedGap) {
			failures = append(failures, fmt.Sprintf("%s: coverage want %v; got %v", path, expectedGap, gap))
			continue
		}
		actual := got[path]
		slices.Sort(actual)
		actual = slices.Compact(actual)
		expected := slices.Clone(cases[path].Want)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) {
			failures = append(failures, fmt.Sprintf("%s: want %v; got %v", path, expected, actual))
		}
	}
	fmt.Printf("Opengrep corpus: %d cases, %d passed, %d failed\n", len(cases), len(cases)-len(failures), len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "\n"))
	}
	return nil
}
