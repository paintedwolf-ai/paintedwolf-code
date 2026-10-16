package providerprofile

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

// ToolCallRejectionKind names what a provider's tool-call parser did with a
// model's native call. Each kind has host handling; the set is closed.
type ToolCallRejectionKind string

const (
	// ToolCallArgumentsUnparsed: the parser dropped the call because its
	// arguments were not valid JSON, and named neither tool nor arguments.
	ToolCallArgumentsUnparsed ToolCallRejectionKind = "arguments_unparsed"
)

// ToolCallRejectionPolicy declares the errors a provider's native tool-call
// parser reports after dropping a model's call. Ship-catalog profiles only;
// a matched error becomes a typed failure and never a call.
type ToolCallRejectionPolicy struct {
	// Profile names the tool_call_rejection_profiles entry the policy came from.
	Profile string `yaml:"-"`
	// Rules are tried in order against the provider's whole error message.
	Rules []ToolCallRejectionRule `yaml:"rules"`
	// StatusBeforeOutput is the HTTP status the provider sends for the same
	// error when nothing was streamed yet. Zero means the error is always in-band.
	StatusBeforeOutput int `yaml:"status_before_output,omitempty"`
}

// ToolCallRejectionRule is one parser error grammar.
type ToolCallRejectionRule struct {
	Kind ToolCallRejectionKind `yaml:"kind"`
	// Grammar is a regular expression for the entire error message; it is
	// anchored at both ends when compiled.
	Grammar string `yaml:"grammar"`
	// Source cites the provider code that produces the error.
	Source   string `yaml:"source"`
	compiled *regexp.Regexp
}

// IsZero reports a provider without declared parser rejections.
func (p ToolCallRejectionPolicy) IsZero() bool { return len(p.Rules) == 0 }

// Compile validates the policy and compiles each grammar in place.
func (p *ToolCallRejectionPolicy) Compile() error {
	if len(p.Rules) == 0 {
		return fmt.Errorf("declares no rules")
	}
	if s := p.StatusBeforeOutput; s != 0 && (s < http.StatusBadRequest || s > 599) {
		return fmt.Errorf("status_before_output %d is not an HTTP error status", s)
	}
	for i := range p.Rules {
		rule := &p.Rules[i]
		if rule.Kind != ToolCallArgumentsUnparsed {
			return fmt.Errorf("rule %d has unsupported kind %q", i, rule.Kind)
		}
		if strings.TrimSpace(rule.Source) == "" {
			return fmt.Errorf("rule %d cites no source", i)
		}
		re, err := regexp.Compile(`^(?:` + rule.Grammar + `)$`)
		if err != nil {
			return fmt.Errorf("rule %d grammar: %w", i, err)
		}
		if re.NumSubexp() > 0 {
			return fmt.Errorf("rule %d grammar captures; %s carries no call identity", i, rule.Kind)
		}
		rule.compiled = re
	}
	return nil
}

// Match returns the kind of the first rule whose grammar spans message.
// Uncompiled rules never match.
func (p ToolCallRejectionPolicy) Match(message string) (ToolCallRejectionKind, bool) {
	for _, rule := range p.Rules {
		if rule.compiled != nil && rule.compiled.MatchString(message) {
			return rule.Kind, true
		}
	}
	return "", false
}

// Classify types a provider error message. Only a request that offered
// callable tools and set no structured output is eligible: a parser error can
// then only come from a dropped call. Everything else returns cause unchanged.
func (p ToolCallRejectionPolicy) Classify(providerID, model, message string, eligible bool, cause error) error {
	if !eligible {
		return cause
	}
	if kind, ok := p.Match(message); ok && kind == ToolCallArgumentsUnparsed {
		return &failure.ProviderToolCallUnparsedError{ProviderID: providerID, Model: model, Cause: cause}
	}
	return cause
}

// ResolveToolCallRejectionProfiles compiles the named profiles.
func ResolveToolCallRejectionProfiles(profiles map[string]ToolCallRejectionPolicy) error {
	for name, policy := range profiles {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("tool_call_rejection_profiles contains an empty name")
		}
		if err := policy.Compile(); err != nil {
			return fmt.Errorf("tool_call_rejection_profile %q: %w", name, err)
		}
		policy.Profile = name
		profiles[name] = policy
	}
	return nil
}
