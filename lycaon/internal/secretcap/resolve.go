package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/ptyinput"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ResolveContext binds a reference to its authorized invocation.
type ResolveContext struct {
	ProjectID, ChatSessionID string
	// ToolName selects the consumer encoding; identity visibility does not depend on it.
	// SessionID and ToolCallID identify telemetry.
	SessionID, ToolName, ToolCallID string
	// Slots restricts reference resolution to declared argument paths.
	// Empty resolves across the entire argument tree.
	Slots []string
}

// ParseReference accepts only a complete capability token.
func ParseReference(reference string) (string, error) {
	id, ok := secretmatch.ParseReferenceToken(reference)
	if !ok || !validID(id) {
		return "", ErrInvalidReference
	}
	return id, nil
}

// ReferenceUse summarizes the reference tokens in an argument tree's string values.
type ReferenceUse struct {
	// Complete reports at least one token the resolver substitutes.
	Complete bool
	// Malformed reports the token prefix outside a complete token. The
	// resolver leaves such text as it is, so it would reach a consumer as a
	// literal instead of the value it names.
	Malformed bool
}

func splitSlotPattern(slot string) []string {
	var parts []string
	for _, chunk := range strings.Split(slot, ".") {
		chunk = strings.TrimSpace(chunk)
		if chunk != "" {
			parts = append(parts, chunk)
		}
	}
	return parts
}

// matchSlotParts reports whether the path lies at or below the slot (exact) or
// on the way to it (prefix); "[]" matches one array index.
func matchSlotParts(patternParts []string, pathParts []string) (exact bool, prefix bool) {
	if len(pathParts) == 0 {
		return false, true
	}
	for i := range min(len(pathParts), len(patternParts)) {
		if patternParts[i] == "[]" {
			if _, err := strconv.Atoi(pathParts[i]); err != nil {
				return false, false
			}
		} else if patternParts[i] != pathParts[i] {
			return false, false
		}
	}
	return len(pathParts) >= len(patternParts), true
}

func pathMatchesSlots(slots []string, path string) (exact bool, prefix bool) {
	if len(slots) == 0 {
		return true, true
	}
	var pathParts []string
	for _, p := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if p != "" {
			unescaped := strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
			pathParts = append(pathParts, unescaped)
		}
	}
	for _, slot := range slots {
		patternParts := splitSlotPattern(slot)
		isExact, isPrefix := matchSlotParts(patternParts, pathParts)
		if isExact {
			exact = true
		}
		if isPrefix {
			prefix = true
		}
	}
	return exact, prefix
}

// ReferenceUseInSlots walks string values permitted by the declared slot paths;
// nil slots permit every value. Map keys are never substituted.
func ReferenceUseInSlots(value any, slots []string) ReferenceUse {
	var use ReferenceUse
	var walk func(any, string)
	walk = func(val any, path string) {
		exact, prefix := pathMatchesSlots(slots, path)
		switch typed := val.(type) {
		case string:
			if exact {
				use.Complete = use.Complete || secretmatch.ContainsReferenceToken(typed)
				use.Malformed = use.Malformed || secretmatch.ContainsMalformedReferenceToken(typed)
			}
		case []string:
			if prefix || exact {
				for i, item := range typed {
					walk(item, path+"/"+strconv.Itoa(i))
				}
			}
		case []any:
			if prefix || exact {
				for i, item := range typed {
					walk(item, path+"/"+strconv.Itoa(i))
				}
			}
		case map[string]any:
			if prefix || exact {
				keys := make([]string, 0, len(typed))
				for k := range typed {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					walk(typed[k], path+"/"+escapePointer(k))
				}
			}
		}
	}
	walk(value, "")
	return use
}

// Resolve substitutes reference tokens in string values of a detached argument tree.
func (s *Service) Resolve(ctx context.Context, args map[string]any, access ResolveContext) (*Resolution, error) {
	r := &Resolution{values: map[string]resolvedValue{}, sources: map[string]string{}, service: s, access: access, unlocks: s.unlocks}
	resolved, err := s.resolveValue(ctx, args, access, r, "")
	if err != nil {
		r.Finish(ctx)
		return nil, err
	}
	out, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("secret resolver produced invalid argument root")
	}
	r.Arguments = out
	return r, nil
}

func (s *Service) resolveValue(ctx context.Context, value any, access ResolveContext, r *Resolution, path string) (any, error) {
	exact, prefix := pathMatchesSlots(access.Slots, path)
	if !exact && !prefix {
		return value, nil
	}
	switch typed := value.(type) {
	case string:
		if !exact {
			return value, nil
		}
		return s.resolveString(ctx, typed, access, r, path)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			next, err := s.resolveValue(ctx, typed[i], access, r, path+"/"+strconv.Itoa(i))
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	case []string:
		out := make([]string, len(typed))
		for i := range typed {
			childPath := path + "/" + strconv.Itoa(i)
			childExact, _ := pathMatchesSlots(access.Slots, childPath)
			if !childExact {
				out[i] = typed[i]
				continue
			}
			next, err := s.resolveString(ctx, typed[i], access, r, childPath)
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next, err := s.resolveValue(ctx, typed[key], access, r, path+"/"+escapePointer(key))
			if err != nil {
				return nil, err
			}
			out[key] = next
		}
		return out, nil
	default:
		return typed, nil
	}
}

func (s *Service) resolveString(ctx context.Context, value string, access ResolveContext, r *Resolution, path string) (string, error) {
	matches := secretmatch.ReferenceTokenIndexes(value)
	if len(matches) == 0 {
		return value, nil
	}
	r.sources[path] = value
	// Offsets refer only to the input; inserted protected bytes are never scanned.
	var resolved strings.Builder
	end := 0
	for _, match := range matches {
		id := value[match[2]:match[3]]
		secret, ok := r.values[id]
		if !ok {
			if len(r.values) >= maxReferencesPerCall {
				return "", &ReferenceLimitError{Count: len(r.values) + 1, Limit: maxReferencesPerCall}
			}
			var err error
			secret, err = s.substitute(ctx, id, access)
			if err != nil {
				return "", err
			}
			secret.useID = s.recordUse(ctx, id, secret.version, UseResolved, access)
			r.values[id] = secret
		}
		r.bindings = append(r.bindings, binding{path: path, id: id})
		resolved.WriteString(value[end:match[0]])
		if path == "/input" && quotesTerminalInput(access.ToolName) {
			resolved.WriteString(ptyinput.QuoteLiteral(secret.value))
		} else {
			resolved.WriteString(secret.value)
		}
		end = match[1]
	}
	resolved.WriteString(value[end:])
	return resolved.String(), nil
}

// substitute validates and loads one capability version.
func (s *Service) substitute(ctx context.Context, id string, access ResolveContext) (resolvedValue, error) {
	row, err := s.queries.GetManagedSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return resolvedValue{}, ErrNotFound
	}
	if err != nil {
		return resolvedValue{}, err
	}
	if err := visible(row, access.ProjectID, access.ChatSessionID); err != nil {
		// Usage stays with the reference's project.
		if row.ProjectID == strings.TrimSpace(access.ProjectID) {
			s.recordUse(ctx, id, 0, UseOutOfScope, access)
		}
		return resolvedValue{}, err
	}
	if row.RevokedAt.Valid {
		s.recordUse(ctx, id, 0, UseRevoked, access)
		return resolvedValue{}, ErrRevoked
	}
	if agentUseEnded(row.AgentUseEndsAt, s.now()) {
		s.recordUse(ctx, id, 0, UseAgentUseExpired, access)
		return resolvedValue{}, ErrAgentUseExpired
	}
	current, hasCurrent, err := s.currentVersion(ctx, id)
	if err != nil {
		return resolvedValue{}, err
	}
	if !hasCurrent {
		s.recordUse(ctx, id, 0, UseUnavailable, access)
		return resolvedValue{}, ErrValueMissing
	}
	entry, ok := s.values.get(current.ID)
	if !ok {
		s.recordUse(ctx, id, current.Version, UseUnavailable, access)
		return resolvedValue{}, ErrValueMissing
	}
	s.rememberValue(access.ChatSessionID, row.Name, id, entry.Value)
	value := resolvedValue{
		id: id, version: current.Version, name: row.Name, value: entry.Value, custody: entry.Custody,
		chatGenerated: entry.GeneratedFor(access.ChatSessionID),
	}
	if s.fingerprint != nil {
		value.fingerprint = s.fingerprint(entry.Value)
	}
	return value, nil
}

// ReferenceLimitError reports the first distinct reference beyond the call bound.
type ReferenceLimitError struct{ Count, Limit int }

func (e *ReferenceLimitError) Error() string { return ErrTooManyReferences.Error() }
func (e *ReferenceLimitError) Unwrap() error { return ErrTooManyReferences }
