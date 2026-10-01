package llm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

const (
	PolicySlotCoordinator = "coordinator"
	PolicySlotAgentPool   = "agent_pool"
	PolicySlotLite        = "lite"
)

type RoleExclusion struct {
	ID        string   `yaml:"id" json:"id"`
	Providers []string `yaml:"providers" json:"providers"`
	Models    []string `yaml:"models" json:"models"`
	Roles     []string `yaml:"roles" json:"roles"`
	Reason    string   `yaml:"reason" json:"reason"`
	Evidence  string   `yaml:"evidence" json:"evidence"`
}

type RoleExclusions struct {
	Rules []RoleExclusion `yaml:"rules" json:"rules"`
}

func LoadRoleExclusions() (RoleExclusions, error) {
	data, err := config.Read(config.ModelRoleExclusions)
	if err != nil {
		return RoleExclusions{}, fmt.Errorf("read model role exclusions: %w", err)
	}
	var doc RoleExclusions
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		return doc, fmt.Errorf("decode model role exclusions: %w", err)
	}
	if doc.Rules == nil {
		return doc, fmt.Errorf("model role exclusions requires rules")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return doc, fmt.Errorf("model role exclusions requires one document")
	}
	seen := map[string]bool{}
	for _, rule := range doc.Rules {
		if rule.ID == "" || seen[rule.ID] || strings.TrimSpace(rule.Reason) == "" || strings.TrimSpace(rule.Evidence) == "" || len(rule.Providers) == 0 || len(rule.Models) == 0 || len(rule.Roles) == 0 {
			return doc, fmt.Errorf("model role exclusion %q requires a unique id, providers, models, roles, reason and evidence", rule.ID)
		}
		seen[rule.ID] = true
		for _, kind := range rule.Providers {
			if strings.TrimSpace(kind) == "" {
				return doc, fmt.Errorf("model role exclusion %q has an empty provider", rule.ID)
			}
		}
		for _, role := range rule.Roles {
			if role != PolicySlotCoordinator && role != PolicySlotAgentPool && role != PolicySlotLite {
				return doc, fmt.Errorf("model role exclusion %q has unknown role %q", rule.ID, role)
			}
		}
		for _, pattern := range rule.Models {
			if strings.TrimSpace(pattern) == "" {
				return doc, fmt.Errorf("model role exclusion %q has an empty pattern", rule.ID)
			}
			if _, err := MatchModelIDPattern(pattern, ""); err != nil {
				return doc, fmt.Errorf("model role exclusion %q: %w", rule.ID, err)
			}
		}
	}
	return doc, nil
}

func cloneRoleExclusions(in RoleExclusions) RoleExclusions {
	out := RoleExclusions{Rules: make([]RoleExclusion, len(in.Rules))}
	for i, rule := range in.Rules {
		rule.Providers = slices.Clone(rule.Providers)
		rule.Models = slices.Clone(rule.Models)
		rule.Roles = slices.Clone(rule.Roles)
		out.Rules[i] = rule
	}
	return out
}

func (doc RoleExclusions) Match(kind, id, role string) *RoleExclusion {
	for _, rule := range doc.Rules {
		if !slices.Contains(rule.Providers, kind) || !slices.Contains(rule.Roles, role) {
			continue
		}
		for _, pattern := range rule.Models {
			matched, err := MatchModelIDPattern(pattern, NormalizeModelIDForExclusion(id))
			if err == nil && matched {
				return &rule
			}
		}
	}
	return nil
}

// NormalizeModelIDForExclusion trims space and a leading models/ prefix.
func NormalizeModelIDForExclusion(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "models/")
	return strings.TrimSpace(id)
}

// roleExclusionRegexPrefix marks full-string regular expressions.
const roleExclusionRegexPrefix = "re:"

// MatchModelIDPattern matches case-insensitive globs or re: regular expressions.
func MatchModelIDPattern(pattern, id string) (bool, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false, nil
	}
	if strings.HasPrefix(pattern, roleExclusionRegexPrefix) {
		expr := strings.TrimSpace(strings.TrimPrefix(pattern, roleExclusionRegexPrefix))
		if expr == "" {
			return false, fmt.Errorf("empty re: pattern")
		}
		if !strings.HasPrefix(expr, "^") {
			expr = "^" + expr
		}
		if !strings.HasSuffix(expr, "$") {
			expr += "$"
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return false, err
		}
		return re.MatchString(id), nil
	}
	re, err := compileModelIDGlob(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(strings.ToLower(id)), nil
}

func compileModelIDGlob(pattern string) (*regexp.Regexp, error) {
	pattern = strings.ToLower(pattern)
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pattern); {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				return nil, fmt.Errorf("consecutive stars in glob")
			}
			i++
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
			i++
		case '\\':
			i++
			if i >= len(pattern) {
				return nil, fmt.Errorf("trailing backslash in glob")
			}
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}
