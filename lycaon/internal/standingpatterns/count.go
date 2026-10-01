package standingpatterns

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

const (
	maxMatchesPerRule   = 50
	maxWalkFilesPerRule = 2000
)

// FlagCount is one standing rule's match total for pack_board orientation.
type FlagCount struct {
	Label     string `json:"label"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated,omitempty"`
}

// CountFlags runs WalkSearch for each rule and returns counts for rules with
// matches. Rules with zero matches are omitted.
func CountFlags(ctx context.Context, projectDir string, rules []Pattern) []FlagCount {
	root := strings.TrimSpace(projectDir)
	if root == "" || len(rules) == 0 {
		return nil
	}
	out := make([]FlagCount, 0, len(rules))
	for _, rule := range rules {
		count, truncated := countRule(ctx, root, rule)
		if count <= 0 {
			continue
		}
		out = append(out, FlagCount{
			Label:     rule.Label,
			Count:     count,
			Truncated: truncated,
		})
	}
	return out
}

func countRule(ctx context.Context, root string, rule Pattern) (int, bool) {
	langName := ""
	if len(rule.Langs) == 1 {
		langName = rule.Langs[0]
	}
	scopes := rule.PathScope
	files, truncated, err := structrewrite.WalkSearch(ctx, structrewrite.WalkRequest{
		Root:       root,
		Pattern:    rule.Pattern,
		LangName:   langName,
		Recursive:  true,
		MaxFiles:   maxWalkFilesPerRule,
		MaxMatches: maxMatchesPerRule,
		PathIncluded: func(relSlash string, isDir bool) bool {
			if isDir {
				return true
			}
			relSlash = filepath.ToSlash(relSlash)
			if len(rule.Langs) > 0 {
				lang, ok := structrewrite.SupportedLanguage("", relSlash)
				if !ok || !langAllowed(lang, rule.Langs) {
					return false
				}
			}
			if len(scopes) == 0 {
				return true
			}
			return pathMatchesScopes(relSlash, scopes)
		},
	})
	if err != nil {
		return 0, false
	}
	total := 0
	for _, f := range files {
		total += len(f.Matches)
	}
	if total > maxMatchesPerRule {
		total = maxMatchesPerRule
		truncated = true
	}
	return total, truncated
}

func pathMatchesScopes(rel string, scopes []string) bool {
	for _, scope := range scopes {
		if pathglob.Match(scope, rel) {
			return true
		}
	}
	return false
}
