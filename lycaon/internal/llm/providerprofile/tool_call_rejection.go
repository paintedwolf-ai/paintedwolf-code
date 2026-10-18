package providerprofile

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
	// ToolCallNotOffered: the parser dropped a call to a tool the request did
	// not offer. The grammar captures the name; the host refuses that name
	// with TOOL_NOT_OFFERED and never executes it.
	ToolCallNotOffered ToolCallRejectionKind = "tool_not_offered"
)

// toolNameToken is the provider function-name token a captured name must be.
var toolNameToken = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// toolCallRejectionCaptures is how many names each kind's grammar captures.
var toolCallRejectionCaptures = map[ToolCallRejectionKind]int{
	ToolCallArgumentsUnparsed: 0,
	ToolCallNotOffered:        1,
}

// ToolCallRejectionPolicy declares the errors a provider's native tool-call
// parser reports after dropping a model's call. Ship-catalog profiles only;
// a matched error becomes a typed failure or a host refusal, never an
// executable call.
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
	Kind       ToolCallRejectionKind `yaml:"kind"`
	Diagnostic string                `yaml:"diagnostic,omitempty"`
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
		captures, known := toolCallRejectionCaptures[rule.Kind]
		if !known {
			return fmt.Errorf("rule %d has unsupported kind %q", i, rule.Kind)
		}
		if rule.Diagnostic != "" && (rule.Kind != ToolCallArgumentsUnparsed || !slices.Contains([]string{"unescaped_newline", "unexpected_end", "invalid_escape"}, rule.Diagnostic)) {
			return fmt.Errorf("rule %d has unsupported diagnostic %q for %s", i, rule.Diagnostic, rule.Kind)
		}
		if strings.TrimSpace(rule.Source) == "" {
			return fmt.Errorf("rule %d cites no source", i)
		}
		re, err := regexp.Compile(`^(?:` + rule.Grammar + `)$`)
		if err != nil {
			return fmt.Errorf("rule %d grammar: %w", i, err)
		}
		if re.NumSubexp() != captures {
			return fmt.Errorf("rule %d grammar captures %d groups; %s captures %d", i, re.NumSubexp(), rule.Kind, captures)
		}
		rule.compiled = re
	}
	return nil
}

// ToolCallRejection is a matched parser error. Tool is the captured name for
// kinds that capture one.
type ToolCallRejection struct {
	Kind       ToolCallRejectionKind
	Tool       string
	Diagnostic string
}

// Match returns the first rule whose grammar spans message. A captured name
// must be a function-name token. Uncompiled rules never match.
func (p ToolCallRejectionPolicy) Match(message string) (ToolCallRejection, bool) {
	for _, rule := range p.Rules {
		if rule.compiled == nil {
			continue
		}
		groups := rule.compiled.FindStringSubmatch(message)
		if groups == nil {
			continue
		}
		rejection := ToolCallRejection{Kind: rule.Kind, Diagnostic: rule.Diagnostic}
		if len(groups) > 1 {
			if !toolNameToken.MatchString(groups[1]) {
				continue
			}
			rejection.Tool = groups[1]
		}
		return rejection, true
	}
	return ToolCallRejection{}, false
}

// UnofferedTool returns the name a tool_not_offered rejection reports when it
// lies outside a non-empty offered set. An offered name in this error is a
// provider fault, so it stays an error.
func (p ToolCallRejectionPolicy) UnofferedTool(message string, offered []string) (string, bool) {
	rejection, ok := p.Match(message)
	if !ok || rejection.Kind != ToolCallNotOffered || len(offered) == 0 || slices.Contains(offered, rejection.Tool) {
		return "", false
	}
	return rejection.Tool, true
}

// Recognizes reports whether an eligible request's message is a declared
// rejection the host handles in-turn.
func (p ToolCallRejectionPolicy) Recognizes(message string, offered []string) bool {
	if _, ok := p.UnofferedTool(message, offered); ok {
		return true
	}
	rejection, ok := p.Match(message)
	return ok && rejection.Kind == ToolCallArgumentsUnparsed
}

// Classify types a provider error message. Only a request that offered
// callable tools and set no structured output is eligible: a parser error can
// then only come from a dropped call. Everything else returns cause unchanged.
func (p ToolCallRejectionPolicy) Classify(providerID, model, message string, eligible bool, cause error) error {
	if !eligible {
		return cause
	}
	if rejection, ok := p.Match(message); ok && rejection.Kind == ToolCallArgumentsUnparsed {
		return &failure.ProviderToolCallUnparsedError{ProviderID: providerID, Model: model, Cause: cause, Diagnostic: rejection.Diagnostic}
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
