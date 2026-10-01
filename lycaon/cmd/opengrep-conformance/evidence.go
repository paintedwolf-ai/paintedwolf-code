package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type expectedSpan struct {
	Line      int `yaml:"line"`
	Column    int `yaml:"column"`
	EndLine   int `yaml:"end_line"`
	EndColumn int `yaml:"end_column"`
}

type expectedEvidence struct {
	Rule    string        `yaml:"rule"`
	Primary expectedSpan  `yaml:"primary"`
	Source  *expectedSpan `yaml:"source"`
	Sink    *expectedSpan `yaml:"sink"`
}

func validateCaseEvidence(tc sourceCase) error {
	lines := strings.Split(tc.Source, "\n")
	valid := func(span expectedSpan) bool {
		point := func(line, column int) bool {
			return line > 0 && line <= len(lines) && column > 0 && column <= len(lines[line-1])+1
		}
		return point(span.Line, span.Column) && point(span.EndLine, span.EndColumn) && (span.EndLine > span.Line || span.EndLine == span.Line && span.EndColumn >= span.Column)
	}
	for _, expected := range tc.Evidence {
		if !slices.Contains(tc.Want, expected.Rule) || !valid(expected.Primary) || expected.Source != nil && !valid(*expected.Source) || expected.Sink != nil && !valid(*expected.Sink) {
			return fmt.Errorf("%s: invalid evidence expectation for %s", tc.Name, expected.Rule)
		}
	}
	return nil
}

func shiftCaseEvidence(evidence []expectedEvidence) []expectedEvidence {
	shifted := slices.Clone(evidence)
	for i := range shifted {
		shifted[i].Primary.Line++
		shifted[i].Primary.EndLine++
		if shifted[i].Source != nil {
			value := *shifted[i].Source
			value.Line++
			value.EndLine++
			shifted[i].Source = &value
		}
		if shifted[i].Sink != nil {
			value := *shifted[i].Sink
			value.Line++
			value.EndLine++
			shifted[i].Sink = &value
		}
	}
	return shifted
}

func compareCaseEvidence(report *scanReport, path string, expected []expectedEvidence) error {
	for _, want := range expected {
		if report.Parsed == nil {
			return fmt.Errorf("%s: structured evidence unavailable", path)
		}
		matches := 0
		for _, finding := range report.Parsed.Findings {
			if strings.TrimPrefix(finding.RuleID, "opengrep:") != want.Rule {
				continue
			}
			for _, location := range finding.Locations {
				if location.URI != path || !spanMatches(location, want.Primary) {
					continue
				}
				matches++
				if err := compareFlowEvidence(finding.Dataflow, path, want); err != nil {
					return err
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: %s expected one finding at %+v; got %d", path, want.Rule, want.Primary, matches)
		}
	}
	return nil
}

func compareFlowEvidence(flow *api.SecurityFindingDataflow, path string, want expectedEvidence) error {
	for _, check := range []struct {
		name   string
		span   *expectedSpan
		source bool
	}{{"source", want.Source, true}, {"sink", want.Sink, false}} {
		if check.span == nil {
			continue
		}
		var trace *api.SecurityFindingCallTrace
		if flow != nil {
			if check.source {
				trace = flow.Source
			} else {
				trace = flow.Sink
			}
		}
		for trace != nil && trace.Callee != nil {
			trace = trace.Callee
		}
		if trace == nil || trace.Location.URI != path || !spanMatches(trace.Location, *check.span) {
			return fmt.Errorf("%s: %s %s evidence does not match %+v", path, want.Rule, check.name, *check.span)
		}
	}
	return nil
}

func spanMatches(location api.SecurityFindingLocation, expected expectedSpan) bool {
	return location.StartLine == expected.Line && location.StartColumn == expected.Column && location.EndLine == expected.EndLine && location.EndColumn == expected.EndColumn
}
