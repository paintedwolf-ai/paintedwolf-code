package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repomap"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/odvcencio/gotreesitter/grammars"
)

type definitionLanguageFixture struct {
	language string
	source   string
}

func TestSourceDefinitionUsesSharedDeclarationModel(t *testing.T) {
	t.Parallel()
	body := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/project/source_symbols.go")
	for _, shared := range []string{"fileoutline.AnalyzeText", "analysis.Definitions"} {
		if !strings.Contains(body, shared) {
			t.Fatalf("editor definition navigation missing shared analysis %q", shared)
		}
	}
	for _, duplicate := range []string{
		"gotreesitter.NewParser",
		"extractSourceSymbols",
		"symbolFromNode",
	} {
		if strings.Contains(body, duplicate) {
			t.Fatalf("editor definition navigation must not carry duplicate declaration logic %q", duplicate)
		}
	}
}

// Serial fixtures avoid competing grammar initialization within parser deadlines.
func TestSourceDefinitionSupportedLanguageMatrix(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	fixtures := readDefinitionLanguageFixtures(t, filepath.Join(
		root,
		"lycaon",
		"internal",
		"structrewrite",
		"language_matrix_test.go",
	))
	if len(fixtures) != len(filekind.SupportedLanguages()) {
		t.Fatalf("definition fixtures = %d, supported languages = %d", len(fixtures), len(filekind.SupportedLanguages()))
	}

	for _, fixture := range fixtures {
		t.Run(fixture.language, func(t *testing.T) {
			spans, _, ok, err := repomap.DefinitionSpans(t.Context(), fixture.language, "", []byte(fixture.source))
			contractcheck.FailErr(t, "analyze supported-language definitions", err)
			if !ok || len(spans) == 0 {
				t.Fatalf("shared definition spans are empty for supported language %q", fixture.language)
			}
			symbols, err := project.SourceSymbolsForContent(t.Context(),
				definitionFixtureFilename(t, fixture.language),
				[]byte(fixture.source),
			)
			contractcheck.FailErr(t, "project supported-language symbols", err)
			got := make(map[string]struct{}, len(symbols))
			for _, symbol := range symbols {
				got[definitionLocationKey(symbol.Name, symbol.Line)] = struct{}{}
			}
			for _, span := range spans {
				key := definitionLocationKey(span.Name, span.StartRow+1)
				if _, exists := got[key]; !exists {
					t.Fatalf("editor navigation dropped %q at line %d (definition kind %q)",
						span.Name, span.StartRow+1, span.Kind)
				}
			}
		})
	}
}

func readDefinitionLanguageFixtures(t *testing.T, path string) []definitionLanguageFixture {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	contractcheck.FailErr(t, "parse supported-language matrix", err)

	var fixtures []definitionLanguageFixture
	ast.Inspect(parsed, func(node ast.Node) bool {
		values, ok := node.(*ast.ValueSpec)
		if !ok || len(values.Names) != 1 || values.Names[0].Name != "languageShapeMatrix" || len(values.Values) != 1 {
			return true
		}
		matrix, ok := values.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, element := range matrix.Elts {
			entry, ok := element.(*ast.CompositeLit)
			if !ok || len(entry.Elts) < 2 {
				continue
			}
			languageLiteral, languageOK := entry.Elts[0].(*ast.BasicLit)
			sourceLiteral, sourceOK := entry.Elts[1].(*ast.BasicLit)
			if !languageOK || !sourceOK {
				continue
			}
			language, languageErr := strconv.Unquote(languageLiteral.Value)
			source, sourceErr := strconv.Unquote(sourceLiteral.Value)
			if languageErr != nil || sourceErr != nil {
				continue
			}
			fixtures = append(fixtures, definitionLanguageFixture{language: language, source: source})
		}
		return false
	})
	if len(fixtures) == 0 {
		t.Fatal("supported-language matrix contained no fixtures")
	}
	return fixtures
}

func definitionFixtureFilename(t *testing.T, language string) string {
	t.Helper()
	if language == "dockerfile" {
		return "Dockerfile"
	}
	entry := grammars.DetectLanguageByName(language)
	if entry == nil || len(entry.Extensions) == 0 {
		t.Fatalf("supported language %q has no filename extension", language)
	}
	return "fixture" + entry.Extensions[0]
}

func definitionLocationKey(name string, line int) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(name), line)
}
