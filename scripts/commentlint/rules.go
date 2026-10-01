package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func inspect(path string, item comment, opts options) []finding {
	var findings []finding
	add := func(rule, message string) {
		if suppressed(item.Text, rule) {
			return
		}
		findings = append(findings, finding{
			Rule: rule, Path: filepath.ToSlash(path), Line: item.StartRow,
			Column: item.StartCol, Language: item.Language, Kind: item.Kind,
			Message: message, Comment: compact(item.Text),
		})
	}
	if opts.workMarkersRequireIssue && workMarkerPattern.MatchString(item.Text) && !opts.issuePattern.MatchString(item.Text) {
		add("work-marker", "work marker has no issue reference")
	}
	if opts.maxLineLength > 0 {
		if line, length := longestLine(item.Text); length > opts.maxLineLength {
			add("line-length", fmt.Sprintf("comment line %d has %d characters", line, length))
		}
	}
	if opts.localHomePath != nil {
		if match := opts.localHomePath.FindString(item.Text); match != "" {
			add("local-home-path", fmt.Sprintf("remove local path %q", match))
		}
	}
	for _, rule := range opts.forbidden {
		if match := rule.pattern.FindString(item.Text); match != "" {
			add("forbid:"+rule.name, fmt.Sprintf("forbidden comment text %q", match))
		}
	}
	return findings
}

func suppressed(text, rule string) bool {
	return strings.Contains(strings.ToLower(text), "commentlint:allow "+strings.ToLower(rule))
}

func longestLine(text string) (line, length int) {
	for i, value := range strings.Split(text, "\n") {
		width := utf8.RuneCountInString(value)
		if width > length {
			line, length = i+1, width
		}
	}
	return line, length
}

func compact(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 180 {
		return text
	}
	return text[:177] + "..."
}
