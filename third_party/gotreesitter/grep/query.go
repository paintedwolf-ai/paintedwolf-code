package grep

import (
	"fmt"
	"sort"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// LangResolver maps a language name to a tree-sitter Language object.
// It returns nil if the language is not available.
type LangResolver func(name string) *gotreesitter.Language

// QueryResult holds the results of executing a full structural grep query.
type QueryResult struct {
	// Matches contains the structural match results after any where-clause
	// filtering has been applied.
	Matches []Result

	// ReplaceResult holds the computed edits if a replace clause was present
	// in the query. It is nil when no replace clause was specified.
	ReplaceResult *ReplaceResult
}

// RunQuery executes a full structural grep query string against source code.
// The resolver maps language names (e.g., "go", "javascript") to Language
// objects.
//
// The query string follows the format:
//
//	find <lang>::<pattern> [where { <constraints> }] [replace { <template> }]
//
// The pipeline:
//  1. Parse the query string
//  2. Resolve the language using the resolver
//  3. Match the pattern against the source
//  4. Apply where-clause filtering (if present)
//  5. Compute replacement edits (if present)
func RunQuery(query string, source []byte, resolver LangResolver) (*QueryResult, error) {
	stmt, err := ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("runquery: %w", err)
	}

	// Resolve the language.
	if stmt.Lang == "" {
		return nil, fmt.Errorf("runquery: no language specified in query; use lang::pattern or provide a language")
	}

	var lang *gotreesitter.Language
	if stmt.Lang == "sexp" {
		// S-expression mode requires a concrete language for parsing the
		// source. The resolver is not consulted for "sexp" since it is
		// not a real language. The caller should use RunQueryWithLang for
		// sexp queries that need a specific language.
		return nil, fmt.Errorf("runquery: sexp queries require a concrete language; use RunQueryWithLang")
	}

	lang = resolver(stmt.Lang)
	if lang == nil {
		return nil, fmt.Errorf("runquery: unknown language %q", stmt.Lang)
	}

	return executeQuery(stmt, source, lang)
}

// RunQueryWithLang executes a query when the language is already known.
// The query string can omit the language prefix — the provided lang is used
// directly for parsing and matching.
//
// This is useful for:
//   - Bare patterns without a language prefix (e.g., "func $NAME()")
//   - When the caller already has a Language object from prior detection
func RunQueryWithLang(query string, source []byte, lang *gotreesitter.Language) (*QueryResult, error) {
	if lang == nil {
		return nil, fmt.Errorf("runquerywl: nil language")
	}

	stmt, err := ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("runquerywl: %w", err)
	}

	// If the query specifies a language, resolve it to verify it matches
	// or use the provided lang.
	if stmt.Lang != "" && stmt.Lang != "sexp" {
		// The caller explicitly provided a lang; use it regardless of the
		// query's lang prefix (the prefix serves as documentation).
	}

	return executeQuery(stmt, source, lang)
}

// DefaultResolver returns a LangResolver that uses the grammars registry
// to look up languages by name.
func DefaultResolver() LangResolver {
	return func(name string) *gotreesitter.Language {
		entry := grammars.DetectLanguageByName(name)
		if entry == nil {
			return nil
		}
		return entry.Language()
	}
}

// executeQuery implements the core query pipeline.
func executeQuery(stmt Query, source []byte, lang *gotreesitter.Language) (*QueryResult, error) {
	// Step 1: Match.
	var results []Result
	var err error

	if stmt.Lang == "sexp" {
		results, err = MatchSexp(lang, stmt.Pattern, source)
	} else {
		results, err = Match(lang, stmt.Pattern, source)
	}
	if err != nil {
		return nil, fmt.Errorf("query match: %w", err)
	}

	// Step 2: Apply where-clause filter.
	if stmt.Where != "" {
		filter, err := CompileWhere(stmt.Where)
		if err != nil {
			return nil, fmt.Errorf("query where: %w", err)
		}
		var filtered []Result
		for i := range results {
			if filter(&results[i], source, lang) {
				filtered = append(filtered, results[i])
			}
		}
		results = filtered
	}

	qr := &QueryResult{
		Matches: results,
	}

	// Step 3: Compute replacement edits if a replace clause is present.
	if stmt.Replace != "" && len(results) > 0 {
		rr, err := computeEdits(results, stmt.Replace, source, lang)
		if err != nil {
			return nil, fmt.Errorf("query replace: %w", err)
		}
		qr.ReplaceResult = rr
	}

	return qr, nil
}

// computeEdits builds replacement edits from a set of match results and a
// replacement template. This is used when where-clause filtering has been
// applied, so we cannot delegate to Replace (which re-does its own matching).
func computeEdits(results []Result, replacement string, source []byte, lang *gotreesitter.Language) (*ReplaceResult, error) {
	// Parse the source to get the AST for match root finding.
	tree, err := parseSnippet(lang, source)
	if err != nil {
		return nil, fmt.Errorf("parse source: %w", err)
	}
	bt := gotreesitter.Bind(tree)
	defer bt.Release()

	root := bt.RootNode()
	if root == nil {
		return &ReplaceResult{}, nil
	}

	origErrors := countErrorNodes(root)

	type candidate struct {
		startByte uint32
		endByte   uint32
		edit      Edit
	}
	var candidates []candidate

	for _, r := range results {
		if len(r.Captures) == 0 {
			continue
		}

		// Build capture text map for template substitution.
		capTexts := make(map[string]string, len(r.Captures))
		for name, cap := range r.Captures {
			capTexts[name] = string(cap.Text)
		}

		// Find the match root — smallest AST node enclosing all captures.
		matchRoot := findMatchRoot(root, r.StartByte, r.EndByte)
		matchStart := r.StartByte
		matchEnd := r.EndByte
		if matchRoot != nil {
			matchStart = matchRoot.StartByte()
			matchEnd = matchRoot.EndByte()
		}

		// Apply template substitution.
		replaced := substituteTemplate(replacement, capTexts, true)

		candidates = append(candidates, candidate{
			startByte: matchStart,
			endByte:   matchEnd,
			edit: Edit{
				StartByte:   matchStart,
				EndByte:     matchEnd,
				Replacement: []byte(replaced),
			},
		})
	}

	if len(candidates) == 0 {
		return &ReplaceResult{}, nil
	}

	// Sort candidates by StartByte ascending, preferring outermost (larger
	// span) when starts are equal.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].startByte == candidates[j].startByte {
			return candidates[i].endByte > candidates[j].endByte
		}
		return candidates[i].startByte < candidates[j].startByte
	})

	// Filter overlapping matches.
	var rr ReplaceResult
	rr.Edits = append(rr.Edits, candidates[0].edit)
	lastEnd := candidates[0].endByte

	for i := 1; i < len(candidates); i++ {
		c := candidates[i]
		if c.startByte < lastEnd {
			rr.Diagnostics = append(rr.Diagnostics, Diagnostic{
				Message:   "overlapping match discarded",
				StartByte: c.startByte,
				EndByte:   c.endByte,
			})
			continue
		}
		rr.Edits = append(rr.Edits, c.edit)
		lastEnd = c.endByte
	}

	// Validate output.
	newSource := ApplyEdits(source, rr.Edits)
	newErrors := countSourceErrors(lang, newSource)
	if newErrors > origErrors {
		rr.Diagnostics = append(rr.Diagnostics, Diagnostic{
			Message: fmt.Sprintf(
				"rewrite introduced %d new parse error(s)",
				newErrors-origErrors,
			),
		})
	}

	return &rr, nil
}
