package grep

import (
	"fmt"
	"regexp"
	"strings"
)

// MetaVar describes a metavariable found in a code pattern.
type MetaVar struct {
	// Name is the user-facing name (e.g., "NAME", "PARAMS", "_").
	Name string

	// Placeholder is the language-valid identifier that replaced the
	// metavariable in the preprocessed pattern (e.g., "__GREP_CAP_NAME__").
	Placeholder string

	// Variadic is true for $$$ captures (zero or more).
	Variadic bool

	// Wildcard is true for $_ (anonymous wildcard).
	Wildcard bool

	// TypeConstraint is the node type constraint for $NAME:type captures,
	// or empty if unconstrained.
	TypeConstraint string
}

// reservedPrefix is the prefix used by all generated placeholders. Patterns
// must not contain this prefix in their raw text (before substitution) to
// avoid ambiguity.
const reservedPrefix = "__GREP_"

// metaVarRe matches the metavariable forms in order of priority:
//
//	$$$NAME   — variadic capture  (group 1 = name)
//	$_        — wildcard           (group 2 = literal "_")
//	$NAME     — single capture     (group 3 = name)
//	$NAME:type — typed capture     (group 3 = name, group 4 = type)
//
// The wildcard branch uses a word-boundary anchor (\b) so that $_foo matches
// as $_ followed by "foo" rather than as the capture variable $_foo. The
// alternation order means the first matching branch wins.
var metaVarRe = regexp.MustCompile(
	`\$\$\$([A-Za-z_]\w*)` + // group 1: variadic name
		`|\$(_)\b` + // group 2: wildcard (_)
		`|\$([A-Za-z_]\w*)(?::([A-Za-z_]\w*))?`, // group 3: name, group 4: type
)

// Preprocess replaces metavariables in a code pattern with language-valid
// placeholder identifiers so tree-sitter can parse the result.
//
// It returns the modified pattern, a map from placeholder identifier to
// [MetaVar] descriptor, and any error encountered.
//
// Metavariable conventions:
//
//	$NAME       → __GREP_CAP_NAME__    (single capture)
//	$$$ITEMS    → __GREP_VAR_ITEMS__   (variadic capture)
//	$_          → __GREP_WILD_1__      (wildcard, numbered for uniqueness)
//	$NAME:type  → __GREP_TYPED_NAME__T__type__ (typed capture)
func Preprocess(pattern string) (string, map[string]*MetaVar, error) {
	// Check for reserved prefix in the raw pattern before any substitution.
	if strings.Contains(pattern, reservedPrefix) {
		return "", nil, fmt.Errorf(
			"pattern contains reserved prefix %q; rename identifiers to avoid collision",
			reservedPrefix,
		)
	}

	mvars := make(map[string]*MetaVar)
	wildSeq := 0

	// Track already-seen named captures so repeated use of the same
	// metavariable produces the same placeholder.
	seen := make(map[string]string) // canonical key → placeholder

	// Build the result by walking through all submatch indices.
	matches := metaVarRe.FindAllStringSubmatchIndex(pattern, -1)
	if len(matches) == 0 {
		return pattern, mvars, nil
	}

	var b strings.Builder
	prev := 0

	for _, loc := range matches {
		// loc[0]:loc[1] is the full match.
		b.WriteString(pattern[prev:loc[0]])

		switch {
		case loc[2] >= 0:
			// Group 1 matched → variadic $$$NAME.
			name := pattern[loc[2]:loc[3]]
			key := "$$$" + name
			ph, ok := seen[key]
			if !ok {
				ph = "__GREP_VAR_" + name + "__"
				mvars[ph] = &MetaVar{
					Name:        name,
					Placeholder: ph,
					Variadic:    true,
				}
				seen[key] = ph
			}
			b.WriteString(ph)

		case loc[4] >= 0:
			// Group 2 matched → wildcard $_.
			wildSeq++
			ph := fmt.Sprintf("__GREP_WILD_%d__", wildSeq)
			mvars[ph] = &MetaVar{
				Name:        "_",
				Placeholder: ph,
				Wildcard:    true,
			}
			b.WriteString(ph)

		case loc[6] >= 0:
			// Group 3 matched → single capture $NAME or typed $NAME:type.
			name := pattern[loc[6]:loc[7]]
			typeConstraint := ""
			if loc[8] >= 0 {
				typeConstraint = pattern[loc[8]:loc[9]]
			}

			var ph string
			if typeConstraint != "" {
				key := "$" + name + ":" + typeConstraint
				existing, ok := seen[key]
				if ok {
					ph = existing
				} else {
					ph = "__GREP_TYPED_" + name + "__T__" + typeConstraint + "__"
					mvars[ph] = &MetaVar{
						Name:           name,
						Placeholder:    ph,
						TypeConstraint: typeConstraint,
					}
					seen[key] = ph
				}
			} else {
				key := "$" + name
				existing, ok := seen[key]
				if ok {
					ph = existing
				} else {
					ph = "__GREP_CAP_" + name + "__"
					mvars[ph] = &MetaVar{
						Name:        name,
						Placeholder: ph,
					}
					seen[key] = ph
				}
			}
			b.WriteString(ph)
		}

		prev = loc[1]
	}

	b.WriteString(pattern[prev:])
	return b.String(), mvars, nil
}
