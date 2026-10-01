package secretmint

import (
	"slices"
	"strings"
)

// Structured arguments use environment-key recognition.
func (i *Inspector) inspectStructured(value any) []Candidate {
	return i.inspectStructuredKeys(value, i.envKeys)
}

func (i *Inspector) inspectStructuredKeys(value any, keys map[string]string) []Candidate {
	var hits []Candidate
	switch value := value.(type) {
	case map[string]any:
		names := make([]string, 0, len(value))
		for key := range value {
			names = append(names, key)
		}
		slices.Sort(names)
		for _, key := range names {
			if text, ok := value[key].(string); ok {
				if canonical, match := i.credentialSlot(key, keys); match {
					hits = append(hits, i.candidate(canonical, text)...)
				}
			} else {
				hits = append(hits, i.inspectStructuredKeys(value[key], keys)...)
			}
		}
	case map[string]string:
		names := make([]string, 0, len(value))
		for key := range value {
			names = append(names, key)
		}
		slices.Sort(names)
		for _, key := range names {
			if canonical, match := i.credentialSlot(key, keys); match {
				hits = append(hits, i.candidate(canonical, value[key])...)
			}
		}
	case []any:
		for _, child := range value {
			hits = append(hits, i.inspectStructuredKeys(child, keys)...)
		}
	}
	return hits
}

// Generic long-flag recognition continues past wrapper delimiters to inspect child argv.
func (i *Inspector) collectCredentialArguments(args []string) []Candidate {
	var hits []Candidate
	for idx := 0; idx < len(args); idx++ {
		token := args[idx]
		if key, value, ok := splitEnvAssignment(token); ok {
			hits = append(hits, i.hitEnv(key, value)...)
			continue
		}
		if !strings.HasPrefix(token, "--") || token == "--" {
			continue
		}
		name, value, attached := splitFlag(token)
		canonical, ok := i.credentialSlot(name, i.flags)
		if !ok {
			continue
		}
		if !attached {
			if idx+1 == len(args) || strings.HasPrefix(args[idx+1], "--") {
				continue
			}
			idx++
			value = args[idx]
		}
		hits = append(hits, i.candidate(canonical, value)...)
	}
	return hits
}

func uniqueCandidates(hits []Candidate) []Candidate {
	type identity struct{ assignment, value string }
	seen := map[identity]bool{}
	out := hits[:0]
	for _, hit := range hits {
		key := identity{hit.Assignment, hit.Value}
		if !seen[key] {
			seen[key] = true
			out = append(out, hit)
		}
	}
	return out
}
