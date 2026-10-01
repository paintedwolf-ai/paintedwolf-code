package orchestration

import (
	"fmt"
	"sort"
	"strings"
)

// aggregateFanOutResults merges subtask leg outputs per the configured mode.
func aggregateFanOutResults(mode AggregationMode, outputs []string) string {
	switch mode {
	case AggregationUnion:
		return aggregateUnion(outputs)
	case AggregationIntersect:
		return aggregateIntersect(outputs)
	case AggregationVote:
		return aggregateVote(outputs)
	case AggregationMerge:
		return aggregateMerge(outputs)
	default:
		return aggregateMerge(outputs)
	}
}

func aggregateUnion(outputs []string) string {
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	for _, out := range outputs {
		for _, line := range splitNonEmptyLines(out) {
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func aggregateIntersect(outputs []string) string {
	if len(outputs) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, out := range outputs {
		seen := make(map[string]struct{})
		for _, line := range splitNonEmptyLines(out) {
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			counts[line]++
		}
	}
	lines := make([]string, 0)
	for line, count := range counts {
		if count == len(outputs) {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func aggregateVote(outputs []string) string {
	best := ""
	bestTokens := -1
	for _, out := range outputs {
		tokens := len(strings.Fields(strings.TrimSpace(out)))
		if tokens > bestTokens {
			bestTokens = tokens
			best = out
		}
	}
	return strings.TrimSpace(best)
}

func aggregateMerge(outputs []string) string {
	var b strings.Builder
	for i, out := range outputs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "## Subtask %d\n\n%s", i, strings.TrimSpace(out))
	}
	return b.String()
}

func splitNonEmptyLines(text string) []string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
