package secretcap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/ptyinput"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type resolvedValue struct {
	id, name, value, useID string
	version                int64
	// chatGenerated marks a value the host generated for this chat. Chat scope
	// is visible only from its own chat, so this chat is the one that holds it.
	chatGenerated bool
}

type binding struct{ path, id string }

// executionRedaction is shorter than any admitted secret, so it cannot
// reproduce one.
const executionRedaction = "***"

// quotesTerminalInput reports whether tool types its input into a held
// terminal, where resolved values are encoded as literal keystrokes.
func quotesTerminalInput(tool string) bool {
	contract, ok := toolcontract.Lookup(tool)
	return ok && contract.Supports(toolcontract.CapabilityHeldTerminalInput)
}

// Resolution owns one invocation's immutable version snapshot and private provenance.
type Resolution struct {
	Arguments         map[string]any `json:"-"`
	values            map[string]resolvedValue
	bindings          []binding
	sources           map[string]string
	service           *Service
	access            ResolveContext
	mu                sync.Mutex
	delivery          map[string]deliveryState
	permissions       []invocationPermission
	localConnectPorts []uint16
}

// Format prevents accidental diagnostic formatting of execution material.
func (r *Resolution) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("<secret resolution>"))
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// Substitute uses only versions already resolved for this invocation.
func (r *Resolution) Substitute(text string) (string, error) {
	if r == nil {
		return text, nil
	}
	var missing bool
	out := secretmatch.ReplaceReferenceTokens(text, func(id string) string {
		value, ok := r.values[id]
		if !ok {
			missing = true
			return secretmatch.ReferenceToken(id)
		}
		return value.value
	})
	if missing {
		return "", ErrValueMissing
	}
	return out, nil
}

// HasUnsafeFileBytes reports whether any resolved secret value contains characters
// unsafe for line-oriented configuration files (newlines or NUL bytes).
func (r *Resolution) HasUnsafeFileBytes() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.values {
		if strings.ContainsAny(v.value, "\r\n\x00") {
			return true
		}
	}
	return false
}

// TestResolvedValue describes a secret value for testing resolution mechanics.
type TestResolvedValue struct {
	ID, Name, Value, Path string
	ChatGenerated         bool
}

// NewResolutionForTest constructs a Resolution for testing.
func NewResolutionForTest(args map[string]any, values []TestResolvedValue) *Resolution {
	r := &Resolution{
		Arguments: args,
		values:    make(map[string]resolvedValue, len(values)),
		sources:   make(map[string]string),
	}
	for _, v := range values {
		r.values[v.ID] = resolvedValue{
			id:            v.ID,
			name:          v.Name,
			value:         v.Value,
			chatGenerated: v.ChatGenerated,
		}
		if v.Path != "" {
			r.bindings = append(r.bindings, binding{path: v.Path, id: v.ID})
			r.sources[v.Path] = secretmatch.ReferenceToken(v.ID)
		}
	}
	return r
}

// Matches derives evidence from resolution, independent of wire encoding.
func (r *Resolution) Matches(matcher *secretmatch.Matcher, included func(string) bool) ([]secretmatch.Match, error) {
	if r == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []secretmatch.Match
	for _, b := range r.bindings {
		if !included(b.path) || seen[b.id] {
			continue
		}
		seen[b.id] = true
		v := r.values[b.id]
		match, err := matcher.ManagedValue(v.value, v.name, secretmatch.ReferenceToken(v.id))
		if err != nil {
			return nil, err
		}
		out = append(out, match)
	}
	return out, nil
}

// GeneratedForChat reports whether every match is exact evidence for a value
// this invocation resolved from a secret the host generated for the chat.
func (r *Resolution) GeneratedForChat(matches []secretmatch.Match) bool {
	if r == nil || len(matches) == 0 {
		return false
	}
	for _, match := range matches {
		if !secretmatch.IsManagedRule(match.RuleID) {
			return false
		}
		id, ok := secretmatch.ParseReferenceToken(match.Reference)
		if !ok || !r.values[id].chatGenerated {
			return false
		}
	}
	return true
}

// Binds reports whether this argument field contains an original reference span.
func (r *Resolution) Binds(path string) bool {
	if r == nil {
		return false
	}
	_, ok := r.sources[path]
	return ok
}

// UnattributedMatches excludes shape aliases contained in known values for this consumer.
func (r *Resolution) UnattributedMatches(text string, matches []secretmatch.Match, included func(string) bool) []secretmatch.Match {
	if r == nil {
		return matches
	}
	var protected []string
	for id := range r.selectedIDs(included) {
		protected = append(protected, r.values[id].value)
	}
	return secretmatch.OutsideProtected(text, matches, protected)
}

// RedactArgument rewrites original reference spans before host serialization.
func (r *Resolution) RedactArgument(path, value string) string {
	if r == nil {
		return value
	}
	if original, ok := r.sources[path]; ok {
		value = secretmatch.ReplaceReferenceTokens(original, func(id string) string {
			if _, ok := r.values[id]; ok {
				return executionRedaction
			}
			return secretmatch.ReferenceToken(id)
		})
	}
	return r.RedactKnown(value).(string)
}

// Resolved reports whether this invocation resolved any value, so a consumer
// knows whether its output can echo one.
func (r *Resolution) Resolved() bool {
	return r != nil && len(r.values) > 0
}

// ReferenceValues replaces each value this invocation resolved with its
// reference token, in its plain form and in every serialized spelling.
func (r *Resolution) ReferenceValues(text string) string {
	if r == nil || text == "" {
		return text
	}
	type replacement struct{ from, to string }
	var pairs []replacement
	for id, v := range r.values {
		if v.value == "" {
			continue
		}
		token := secretmatch.ReferenceToken(id)
		pairs = append(pairs, replacement{v.value, token})
		for _, encoded := range secretmatch.EncodedForms(v.value) {
			pairs = append(pairs, replacement{encoded, token})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].from) > len(pairs[j].from) })
	for _, p := range pairs {
		text = strings.ReplaceAll(text, p.from, p.to)
	}
	return text
}

// RedactKnown strips resolved values from structured consumers before serialization.
func (r *Resolution) RedactKnown(value any) any {
	if r == nil {
		return value
	}
	secrets := make([]string, 0, len(r.values))
	for _, v := range r.values {
		secrets = append(secrets, v.value)
		if quotesTerminalInput(r.access.ToolName) {
			secrets = append(secrets, ptyinput.QuoteLiteral(v.value))
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return redactKnown(value, secrets)
}

func redactKnown(value any, secrets []string) any {
	switch v := value.(type) {
	case string:
		for _, secret := range secrets {
			v = strings.ReplaceAll(v, secret, executionRedaction)
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = redactKnown(child, secrets)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = redactKnown(child, secrets)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, child := range v {
			out[i] = redactKnown(child, secrets).(string)
		}
		return out
	default:
		return v
	}
}

type resolutionContextKey struct{}

// WithResolution attaches an invocation's resolution to ctx.
func WithResolution(ctx context.Context, r *Resolution) context.Context {
	return context.WithValue(ctx, resolutionContextKey{}, r)
}

// ResolutionFrom returns the resolution attached by WithResolution, or nil.
func ResolutionFrom(ctx context.Context) *Resolution {
	r, _ := ctx.Value(resolutionContextKey{}).(*Resolution)
	return r
}
