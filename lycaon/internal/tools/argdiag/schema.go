package argdiag

import (
	"sort"
	"strings"
)

// declaredProperties merges an object schema's properties with those of its
// combinator branches.
func declaredProperties(schema map[string]any) map[string]map[string]any {
	if schema == nil {
		return nil
	}
	props := map[string]map[string]any{}
	addProps := func(p any) {
		if m, ok := p.(map[string]any); ok {
			for k, v := range m {
				if vm, ok := v.(map[string]any); ok {
					props[k] = vm
				} else {
					props[k] = map[string]any{}
				}
			}
		}
	}
	if p, ok := schema["properties"]; ok {
		addProps(p)
	}
	for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
		if list, ok := schema[comb].([]any); ok {
			for _, item := range list {
				if branch, ok := item.(map[string]any); ok {
					if p, ok := branch["properties"]; ok {
						addProps(p)
					}
				}
			}
		}
	}
	return props
}

func detectCombinatorConflicts(schema map[string]any, args map[string]any) []string {
	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) < 2 {
		return nil
	}
	var presentBranches [][]string
	for _, b := range branches {
		bMap, ok := b.(map[string]any)
		if !ok {
			continue
		}
		var branchPresent []string
		if reqList, ok := bMap["required"].([]any); ok {
			for _, r := range reqList {
				if rStr, ok := r.(string); ok {
					if _, present := args[rStr]; present {
						branchPresent = append(branchPresent, rStr)
					}
				}
			}
		}
		if len(branchPresent) > 0 {
			presentBranches = append(presentBranches, branchPresent)
		}
	}
	if len(presentBranches) > 1 {
		var allConflicts []string
		for _, b := range presentBranches {
			allConflicts = append(allConflicts, b...)
		}
		sort.Strings(allConflicts)
		return allConflicts
	}
	return nil
}

func schemaExpectsStructuredJSON(propSchema map[string]any) (bool, string) {
	if propSchema == nil {
		return false, ""
	}
	t, _ := propSchema["type"].(string)
	if t == "array" {
		return true, "array"
	}
	if t == "object" {
		return true, "object"
	}
	for _, comb := range []string{"oneOf", "anyOf"} {
		if list, ok := propSchema[comb].([]any); ok {
			for _, item := range list {
				if branch, ok := item.(map[string]any); ok {
					bt, _ := branch["type"].(string)
					if bt == "array" {
						return true, "array"
					}
					if bt == "object" {
						return true, "object"
					}
				}
			}
		}
	}
	return false, ""
}

func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	la := len(ra)
	lb := len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			min := del
			if ins < min {
				min = ins
			}
			if sub < min {
				min = sub
			}
			curr[j] = min
		}
		copy(prev, curr)
	}
	return prev[lb]
}

func findClosestPropertyMatch(unknownTopKeys, topPropKeys []string) (string, string) {
	bestMatch := ""
	bestUnk := ""
	bestSim := 0.0
	minDist := 999
	for _, unk := range unknownTopKeys {
		unkLower := strings.ToLower(unk)
		for _, declared := range topPropKeys {
			declLower := strings.ToLower(declared)
			if unkLower == declLower {
				continue
			}
			d := levenshtein(unkLower, declLower)
			maxLen := len(unkLower)
			if len(declLower) > maxLen {
				maxLen = len(declLower)
			}
			minLen := len(unkLower)
			if len(declLower) < minLen {
				minLen = len(declLower)
			}
			if maxLen == 0 {
				continue
			}
			sim := 1.0 - (float64(d) / float64(maxLen))
			isMatch := false
			if sim >= 0.70 {
				isMatch = true
			} else if d <= 2 && minLen >= 4 {
				isMatch = true
			} else if (strings.HasPrefix(declLower, unkLower) || strings.HasPrefix(unkLower, declLower)) && minLen >= 3 && sim >= 0.65 {
				isMatch = true
			}

			if isMatch && (sim > bestSim || (sim == bestSim && d < minDist)) {
				bestSim = sim
				minDist = d
				bestMatch = declared
				bestUnk = unk
			}
		}
	}
	return bestMatch, bestUnk
}
