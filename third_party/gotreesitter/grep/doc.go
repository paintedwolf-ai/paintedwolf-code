// Package grep provides structural code search, match, and rewrite using
// tree-sitter parse trees. It is an AST-grep-inspired pattern matching
// engine built on gotreesitter's query system.
//
// Code patterns use metavariables ($NAME, $$$ARGS, $_, $E:type) that match
// AST nodes structurally. Patterns are parsed as real code in the target
// language, then compiled to tree-sitter S-expression queries.
//
// Basic usage:
//
//	lang := grammars.DetectLanguageByName("go").Language()
//	results, err := grep.Match(lang, `func $NAME($$$) error`, source)
//	for _, r := range results {
//	    fmt.Printf("found: %s\n", r.Captures["NAME"].Text)
//	}
//
// Rewrite usage:
//
//	result, err := grep.Replace(lang, `$E.unwrap()`, `$E.expect("failed")`, source)
//	output := grep.ApplyEdits(source, result.Edits)
//
// Full query syntax:
//
//	find <lang>::<pattern> [where { <constraints> }] [replace { <template> }]
package grep
