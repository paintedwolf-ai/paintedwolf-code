package detectionpack

import (
	"fmt"
	"regexp"
	"strings"
)

type matchOp int

const (
	opEquals matchOp = iota
	opContains
	opStartsWith
	opEndsWith
	opRegex
)

type fieldMatcher struct {
	field string // canonical Event / EgressEvent field name
	op    matchOp
	all   bool // |all — every configured value must match
	// values are folded to lower case: the same path reaches the host in whatever
	// case its runtime printed.
	values []string
	// raw keeps the author's casing for callers reading a rule as a catalogue
	// rather than as a matcher — `~/Library/Keychains` is a path, the fold is not.
	raw     []string
	regexps []*regexp.Regexp
}

type selection struct {
	matchers []fieldMatcher
}

func (s selection) match(lookup func(string) (any, bool)) bool {
	for _, m := range s.matchers {
		got, ok := lookup(m.field)
		if !ok || !m.match(got) {
			return false
		}
	}
	return true
}

func (m fieldMatcher) match(got any) bool {
	var elems []string
	switch v := got.(type) {
	case string:
		elems = []string{v}
	case []string:
		elems = v
	default:
		return false
	}
	if m.op == opRegex {
		return m.matchRegex(elems)
	}
	lowered := make([]string, len(elems))
	for i, e := range elems {
		lowered[i] = strings.ToLower(e)
	}
	if m.all {
		for _, want := range m.values {
			if !anyElemMatches(lowered, want, m.op) {
				return false
			}
		}
		return len(m.values) > 0
	}
	for _, want := range m.values {
		if anyElemMatches(lowered, want, m.op) {
			return true
		}
	}
	return false
}

func (m fieldMatcher) matchRegex(elems []string) bool {
	if m.all {
		// Rejected at compile time.
		return false
	}
	for _, re := range m.regexps {
		for _, e := range elems {
			if re.MatchString(e) {
				return true
			}
		}
	}
	return false
}

func anyElemMatches(elems []string, want string, op matchOp) bool {
	for _, e := range elems {
		switch op {
		case opEquals:
			if e == want {
				return true
			}
		case opContains:
			if strings.Contains(e, want) {
				return true
			}
		case opStartsWith:
			if strings.HasPrefix(e, want) {
				return true
			}
		case opEndsWith:
			if strings.HasSuffix(e, want) {
				return true
			}
		case opRegex:
		}
	}
	return false
}

var allowedModifiers = map[string]struct{}{
	"contains":   {},
	"startswith": {},
	"endswith":   {},
	"re":         {},
	"all":        {},
}

func compileFieldMatcher(key string, raw any, allowedFields map[string]struct{}) (fieldMatcher, error) {
	parts := strings.Split(key, "|")
	field := parts[0]
	if field == "" {
		return fieldMatcher{}, fmt.Errorf("unknown field: %s", key)
	}
	if _, ok := allowedFields[field]; !ok {
		return fieldMatcher{}, fmt.Errorf("unknown field: %s", field)
	}

	var mods []string
	if len(parts) > 1 {
		mods = parts[1:]
	}
	op := opEquals
	all := false
	hasRe := false
	hasMatchMod := false
	for _, mod := range mods {
		mod = strings.ToLower(strings.TrimSpace(mod))
		if _, ok := allowedModifiers[mod]; !ok {
			return fieldMatcher{}, fmt.Errorf("unsupported modifier: %s", mod)
		}
		switch mod {
		case "contains":
			if hasMatchMod && op != opContains {
				return fieldMatcher{}, fmt.Errorf("unsupported modifier combination: %s", mod)
			}
			op = opContains
			hasMatchMod = true
		case "startswith":
			if hasMatchMod && op != opStartsWith {
				return fieldMatcher{}, fmt.Errorf("unsupported modifier combination: %s", mod)
			}
			op = opStartsWith
			hasMatchMod = true
		case "endswith":
			if hasMatchMod && op != opEndsWith {
				return fieldMatcher{}, fmt.Errorf("unsupported modifier combination: %s", mod)
			}
			op = opEndsWith
			hasMatchMod = true
		case "re":
			if hasMatchMod && op != opRegex {
				return fieldMatcher{}, fmt.Errorf("unsupported modifier combination: %s", mod)
			}
			op = opRegex
			hasRe = true
			hasMatchMod = true
		case "all":
			all = true
		}
	}
	if hasRe && all {
		return fieldMatcher{}, fmt.Errorf("unsupported modifier combination: re+all")
	}
	if all && op == opEquals {
		return fieldMatcher{}, fmt.Errorf("unsupported modifier: all")
	}

	values, err := coerceStringList(raw)
	if err != nil {
		return fieldMatcher{}, err
	}
	if len(values) == 0 {
		return fieldMatcher{}, fmt.Errorf("empty value for field %s", field)
	}

	fm := fieldMatcher{field: field, op: op, all: all}
	if op == opRegex {
		fm.regexps = make([]*regexp.Regexp, 0, len(values))
		for _, v := range values {
			re, err := regexp.Compile(v)
			if err != nil {
				return fieldMatcher{}, fmt.Errorf("unsupported modifier: re (%w)", err)
			}
			fm.regexps = append(fm.regexps, re)
		}
		return fm, nil
	}
	fm.raw = append([]string(nil), values...)
	fm.values = make([]string, len(values))
	for i, v := range values {
		fm.values[i] = strings.ToLower(v)
	}
	if err := validateTypedSelectionValues(field, fm.values); err != nil {
		return fieldMatcher{}, err
	}
	return fm, nil
}

func coerceStringList(raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("field values must be strings")
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return append([]string(nil), v...), nil
	case nil:
		return nil, fmt.Errorf("empty value")
	default:
		return nil, fmt.Errorf("field values must be strings")
	}
}
