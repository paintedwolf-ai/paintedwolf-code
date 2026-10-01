// Package standingpatterns loads optional project anti-pattern rules from
// <overlay>/standing-patterns.yaml and counts matches via structrewrite.WalkSearch.
package standingpatterns

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

func configRelPath() string {
	return settingsoverlay.Rel(settingsoverlay.BasenameStandingPatterns)
}

// Pattern is one standing anti-pattern rule declared by the project.
type Pattern struct {
	ID        string   `yaml:"id"`
	Label     string   `yaml:"label"`
	Pattern   string   `yaml:"pattern"`
	Langs     []string `yaml:"langs"`
	PathScope []string `yaml:"path_scope"`
}

// SkippedRule records a rule that failed validation at load time.
type SkippedRule struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Config is the loaded standing-pattern rules for one project.
type Config struct {
	Rules   []Pattern
	Skipped []SkippedRule
}

type fileShape struct {
	Patterns []Pattern `yaml:"patterns"`
}

// Load reads <overlay>/standing-patterns.yaml under projectDir. Missing file
// returns an empty config. Invalid rules are skipped and listed in Skipped.
func Load(ctx context.Context, projectDir string) (Config, error) {
	root := strings.TrimSpace(projectDir)
	if root == "" {
		return Config{}, nil
	}
	path := filepath.Join(root, filepath.FromSlash(configRelPath()))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("standing patterns: read %s: %w", configRelPath(), err)
	}
	var raw fileShape
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("standing patterns: parse %s: %w", configRelPath(), err)
	}
	return validateConfig(ctx, raw.Patterns)
}

func validateConfig(ctx context.Context, raw []Pattern) (Config, error) {
	if len(raw) == 0 {
		return Config{}, nil
	}
	var out Config
	seen := map[string]struct{}{}
	for _, rule := range raw {
		id := strings.TrimSpace(rule.ID)
		label := strings.TrimSpace(rule.Label)
		pattern := strings.TrimSpace(rule.Pattern)
		switch {
		case id == "":
			out.Skipped = append(out.Skipped, SkippedRule{ID: rule.ID, Reason: "id required"})
			continue
		case label == "":
			out.Skipped = append(out.Skipped, SkippedRule{ID: id, Reason: "label required"})
			continue
		case pattern == "":
			out.Skipped = append(out.Skipped, SkippedRule{ID: id, Reason: "pattern required"})
			continue
		}
		if _, ok := seen[id]; ok {
			out.Skipped = append(out.Skipped, SkippedRule{ID: id, Reason: "duplicate id"})
			continue
		}
		seen[id] = struct{}{}
		langs := normalizeLangs(rule.Langs)
		if err := validatePatternCompile(ctx, pattern, langs); err != nil {
			out.Skipped = append(out.Skipped, SkippedRule{ID: id, Reason: err.Error()})
			continue
		}
		scopes := normalizeScopes(rule.PathScope)
		out.Rules = append(out.Rules, Pattern{
			ID:        id,
			Label:     label,
			Pattern:   pattern,
			Langs:     langs,
			PathScope: scopes,
		})
	}
	return out, nil
}

func normalizeLangs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, lang := range in {
		lang = strings.ToLower(strings.TrimSpace(lang))
		if lang == "" {
			continue
		}
		if _, ok := seen[lang]; ok {
			continue
		}
		seen[lang] = struct{}{}
		out = append(out, lang)
	}
	return out
}

func normalizeScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, scope := range in {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		out = append(out, filepath.ToSlash(scope))
	}
	return out
}
