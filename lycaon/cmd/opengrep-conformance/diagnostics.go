package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type expectedDiagnostic struct {
	Kind      api.ScanWarningKind `yaml:"kind" json:"kind"`
	Construct string              `yaml:"construct,omitempty" json:"construct,omitempty"`
	Rule      string              `yaml:"rule,omitempty" json:"rule,omitempty"`
	Line      int                 `yaml:"line,omitempty" json:"line,omitempty"`
	Column    int                 `yaml:"column,omitempty" json:"column,omitempty"`
}

type expectedProjectDiagnostic struct {
	File               string `yaml:"file" json:"file"`
	expectedDiagnostic `yaml:",inline"`
}

func projectDiagnostics(warnings []api.ScanWarning) []expectedProjectDiagnostic {
	var result []expectedProjectDiagnostic
	for _, warning := range warnings {
		result = append(result, expectedProjectDiagnostic{File: strings.TrimPrefix(warning.File, "source/"), expectedDiagnostic: expectedDiagnostic{
			Kind: warning.Kind, Construct: warning.Construct, Rule: warning.RuleID, Line: warning.StartLine, Column: warning.StartColumn,
		}})
	}
	return sortedProjectDiagnostics(result)
}

func sortedProjectDiagnostics(input []expectedProjectDiagnostic) []expectedProjectDiagnostic {
	result := slices.Clone(input)
	slices.SortFunc(result, func(a, b expectedProjectDiagnostic) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Construct, b.Construct), cmp.Compare(a.Rule, b.Rule), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	return result
}

func compareCaseDiagnostics(warnings []api.ScanWarning, cases map[string]sourceCase) error {
	var expected []expectedProjectDiagnostic
	for path, tc := range cases {
		for _, diagnostic := range tc.Diagnostics {
			expected = append(expected, expectedProjectDiagnostic{File: strings.TrimPrefix(path, "source/"), expectedDiagnostic: diagnostic})
		}
	}
	actual := projectDiagnostics(warnings)
	if !slices.Equal(actual, sortedProjectDiagnostics(expected)) {
		return fmt.Errorf("scanner diagnostics: want %+v; got %+v", expected, actual)
	}
	return nil
}
