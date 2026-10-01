package sandbox

import (
	"strings"
)

// ScopeTokens carries ToolContext-sourced values for scope glob template substitution.
type ScopeTokens struct {
	Self string
	Job  string
}

// Scope template tokens substituted from ToolContext before glob matching.
const (
	scopeTokenSelf = "{self}"
	scopeTokenJob  = "{job}"
)

// ValidScopeTokenValue reports whether v is safe to substitute into a scope glob.
func ValidScopeTokenValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if v == ".." {
		return false
	}
	for _, r := range v {
		switch r {
		case '/', '\\', '*', '?', '[', ']':
			return false
		}
	}
	return true
}

// SubstituteScopeGlobs expands {self}/{job} tokens in patterns. Patterns whose
// tokens fail validation are dropped rather than substituted.
func SubstituteScopeGlobs(patterns []string, tokens ScopeTokens) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if resolved, ok := substituteScopeGlob(pattern, tokens); ok {
			out = append(out, resolved)
		}
	}
	return out
}

func substituteScopeGlob(pattern string, tokens ScopeTokens) (string, bool) {
	if !strings.Contains(pattern, scopeTokenSelf) && !strings.Contains(pattern, scopeTokenJob) {
		return pattern, true
	}
	if strings.Contains(pattern, scopeTokenSelf) {
		if !ValidScopeTokenValue(tokens.Self) {
			return "", false
		}
	}
	if strings.Contains(pattern, scopeTokenJob) {
		if !ValidScopeTokenValue(tokens.Job) {
			return "", false
		}
	}
	out := strings.ReplaceAll(pattern, scopeTokenSelf, tokens.Self)
	out = strings.ReplaceAll(out, scopeTokenJob, tokens.Job)
	return out, true
}
