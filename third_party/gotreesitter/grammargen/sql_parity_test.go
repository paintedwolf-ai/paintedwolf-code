package grammargen

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestSQLImportedCorpusSnippetParity(t *testing.T) {
	genLang, refLang := loadGeneratedSQLLanguagesForParity(t)

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "select_literal_list",
			src:  "SELECT 1, 2;\n",
		},
		{
			name: "select_identifier_list",
			src:  "SELECT a, b;\n",
		},
		{
			name: "select_parenthesized_boolean",
			src:  "SELECT (TRUE);\n",
		},
		{
			name: "select_dollar_quoted_string",
			src:  "SELECT $$hey$$;\n",
		},
		{
			name: "insert_multiple_values",
			src:  "INSERT INTO table1 VALUES (1, 'a'), (2, 'b');\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGeneratedAndReferenceDeepParity(t, genLang, refLang, tc.src)
		})
	}
}

func loadGeneratedSQLLanguagesForParity(t *testing.T) (*gotreesitter.Language, *gotreesitter.Language) {
	t.Helper()

	source, err := os.ReadFile(sqlGrammarJSONPathForTest(t))
	if err != nil {
		t.Fatalf("read sql grammar.json: %v", err)
	}
	gram, err := ImportGrammarJSON(source)
	if err != nil {
		t.Fatalf("import sql grammar: %v", err)
	}
	genLang, err := generateWithTimeout(gram, 90*time.Second)
	if err != nil {
		t.Fatalf("generate sql language: %v", err)
	}
	refLang := grammars.SqlLanguage()
	adaptExternalScanner(refLang, genLang)
	return genLang, refLang
}

func sqlGrammarJSONPathForTest(t *testing.T) string {
	t.Helper()

	candidates := []string{
		"/tmp/grammar_parity/sql/src/grammar.json",
		"/tmp/sql-grammar/src/grammar.json",
		".parity_seed/sql/src/grammar.json",
		"../.parity_seed/sql/src/grammar.json",
	}
	globs := []string{
		"/tmp/gotreesitter-parity-*/repos/sql/src/grammar.json",
	}
	for _, pattern := range globs {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			candidates = append(candidates, matches...)
		}
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("SQL grammar.json not available")
	return ""
}
