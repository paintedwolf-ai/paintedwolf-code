package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func explicitFlags() map[string]bool {
	set := map[string]bool{}
	flag.Visit(func(item *flag.Flag) { set[item.Name] = true })
	return set
}

func applyConfig(cfg fileConfig, explicit map[string]bool, maxLine *int, markerIssue *bool, issueExpr *string, localPaths *bool, parseTimeout *time.Duration) {
	if cfg.MaxLineLength != nil && !explicit["max-line-length"] {
		*maxLine = *cfg.MaxLineLength
	}
	if cfg.WorkMarkersRequireIssue != nil && !explicit["work-markers-require-issue"] {
		*markerIssue = *cfg.WorkMarkersRequireIssue
	}
	if cfg.IssuePattern != "" && !explicit["issue-pattern"] {
		*issueExpr = cfg.IssuePattern
	}
	if cfg.LocalHomePaths != nil && !explicit["local-home-paths"] {
		*localPaths = *cfg.LocalHomePaths
	}
	if cfg.ParseTimeout != "" && !explicit["parse-timeout"] {
		parsed, err := time.ParseDuration(cfg.ParseTimeout)
		if err != nil {
			fatal(fmt.Errorf("config parse_timeout: %w", err))
		}
		*parseTimeout = parsed
	}
}

func loadConfig(root, path string) (fileConfig, error) {
	if path == "" {
		return fileConfig{}, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	file, err := os.Open(path) // #nosec G304 -- explicit configuration path
	if err != nil {
		return fileConfig{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	var cfg fileConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return fileConfig{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

func configForbidden(patterns map[string]string) []string {
	names := make([]string, 0, len(patterns))
	for name := range patterns {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, name+"="+patterns[name])
	}
	return values
}

func compileForbidden(values []string) ([]namedPattern, error) {
	namePattern := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	out := make([]namedPattern, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		name, expression, ok := strings.Cut(value, "=")
		if !ok || !namePattern.MatchString(name) || expression == "" {
			return nil, fmt.Errorf("invalid --forbid %q; want name=regexp", value)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate forbid rule %q", name)
		}
		seen[name] = true
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("forbid %s: %w", name, err)
		}
		out = append(out, namedPattern{name: name, pattern: pattern})
	}
	return out, nil
}

func resolveRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isRepoRoot(current) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("repository root not found; pass --root")
		}
		current = parent
	}
}

func isRepoRoot(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func homePathPattern() *regexp.Regexp {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(filepath.Clean(home)))
}
