package main

import (
	"path/filepath"
	"slices"
	"strings"
)

func classifyProjectFindings(m *projectMeasurement, project projectCase, report *scanReport) {
	seen := map[projectFinding]bool{}
	matched := make([]bool, len(project.Findings))
	for _, finding := range report.Results {
		file := strings.TrimPrefix(filepath.ToSlash(finding.Path), "source/")
		item := projectFinding{File: file, Rule: finding.RuleID, Line: finding.Start.Line, Column: finding.Start.Col}
		if seen[item] {
			m.Duplicates++
			continue
		}
		seen[item] = true
		m.Findings = append(m.Findings, item)
		expected := -1
		for index, want := range project.Findings {
			if !matched[index] && want.File == item.File && want.Rule == item.Rule && want.Line == item.Line && (want.Column == 0 || want.Column == item.Column) {
				expected = index
				break
			}
		}
		if expected >= 0 {
			matched[expected] = true
			m.TruePositive++
			counts := m.Families[item.Rule]
			counts.TruePositive++
			m.Families[item.Rule] = counts
		} else if project.ReviewStatus == "pending" {
			m.Unresolved++
			counts := m.Families[item.Rule]
			counts.Unresolved++
			m.Families[item.Rule] = counts
		} else {
			m.FalsePositive++
			counts := m.Families[item.Rule]
			counts.FalsePositive++
			m.Families[item.Rule] = counts
		}
	}
	for index, found := range matched {
		if !found {
			m.FalseNegative++
			rule := project.Findings[index].Rule
			counts := m.Families[rule]
			counts.FalseNegative++
			m.Families[rule] = counts
		}
	}

	slices.SortFunc(m.Findings, func(a, b projectFinding) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		if a.Column != b.Column {
			return a.Column - b.Column
		}
		return strings.Compare(a.Rule, b.Rule)
	})
}
