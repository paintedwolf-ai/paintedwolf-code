package orchestration

import (
	"fmt"
	"strings"
)

type packLegResult struct {
	Index  int
	Output string
	Failed bool
}

func mergePackResults(strategy MergeStrategy, results []packLegResult) (string, error) {
	switch strategy {
	case MergeFirstValid:
		return mergeFirstValid(results)
	case MergeConsensus:
		return mergeConsensus(results)
	case MergeUnion:
		return mergeUnionPack(results)
	default:
		return mergeFirstValid(results)
	}
}

func mergeFirstValid(results []packLegResult) (string, error) {
	for _, r := range results {
		if r.Failed {
			continue
		}
		if out := strings.TrimSpace(r.Output); out != "" {
			return out, nil
		}
	}
	return "", fmt.Errorf("pack: all probes failed")
}

func mergeConsensus(results []packLegResult) (string, error) {
	counts := make(map[string]int)
	originals := make(map[string]string)
	successes := 0
	for _, r := range results {
		if r.Failed {
			continue
		}
		out := strings.TrimSpace(r.Output)
		if out == "" {
			continue
		}
		successes++
		norm := normalizePackOutput(out)
		counts[norm]++
		if _, ok := originals[norm]; !ok {
			originals[norm] = out
		}
	}
	if successes < 2 {
		return "", fmt.Errorf("pack consensus: insufficient successful outputs")
	}
	for norm, count := range counts {
		if count >= 2 {
			return originals[norm], nil
		}
	}
	return "", fmt.Errorf("pack consensus: no agreement")
}

func mergeUnionPack(results []packLegResult) (string, error) {
	outputs := make([]string, 0, len(results))
	for _, r := range results {
		if r.Failed {
			continue
		}
		if out := strings.TrimSpace(r.Output); out != "" {
			outputs = append(outputs, out)
		}
	}
	if len(outputs) == 0 {
		return "", fmt.Errorf("pack union: no outputs")
	}
	return aggregateUnion(outputs), nil
}

func normalizePackOutput(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}
