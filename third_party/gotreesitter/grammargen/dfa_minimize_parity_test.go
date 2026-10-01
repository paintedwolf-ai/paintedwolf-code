package grammargen

import (
	"os"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestLexMinimizeParityAcrossGrammars proves minimizeLexStates (see
// dfa_minimize.go) never changes observable lexing/parsing behavior. It
// generates each representative grammar twice from the identical Grammar
// struct -- once with GTS_GRAMMARGEN_DISABLE_LEX_MINIMIZE=1 (the pre-fix
// codepath: buildLexDFA's raw per-mode-concatenated LexStates, unmerged) and
// once with minimization enabled (the fix) -- then parses one or more real
// corpus files with both and asserts the resulting parse trees are
// byte-identical S-expressions. Any lexing divergence introduced by merging
// states would necessarily show up as a token/tree difference here.
//
// Corpus files are pulled from an external corpus_sources checkout (real
// tree-sitter grammar-source corpora, e.g. the gotreesitter-corpora sibling
// repo) when available -- real-world source, exercising the full lexical
// surface. Override the root with GOT_CORPUS_SOURCES_ROOT; the test skips a
// grammar's corpus case gracefully if the resulting path isn't present, but
// always still runs the tiny-snippet cases so the parity property is
// checked even without any external corpora checkout.
func TestLexMinimizeParityAcrossGrammars(t *testing.T) {
	type corpusCase struct {
		grammar string
		path    string
	}
	corporaRoot := os.Getenv("GOT_CORPUS_SOURCES_ROOT")
	if corporaRoot == "" {
		if home, err := os.UserHomeDir(); err == nil {
			corporaRoot = home + "/work/gotreesitter-corpora/corpus_sources"
		}
	}
	corpusCases := []corpusCase{
		{"swift", corporaRoot + "/swift/Sources/SwiftSyntaxMacros/MacroExpansionContext.swift"},
		{"swift", corporaRoot + "/swift/Tests/SwiftParserTest/translated/ForwardSlashRegexSkippingAllowedTests.swift"},
		{"kotlin", corporaRoot + "/kotlin/buildSrc/src/main/kotlin/CommunityProjectsBuild.kt"},
		{"kotlin", corporaRoot + "/kotlin/kotlinx-coroutines-core/common/test/sync/MutexTest.kt"},
		{"go", corporaRoot + "/go/src/compress/flate/deflatefast.go"},
		{"go", corporaRoot + "/go/src/runtime/tracestatus.go"},
		{"typescript", corporaRoot + "/typescript/src/testRunner/unittests/tsc/moduleResolution.ts"},
		{"javascript", corporaRoot + "/javascript/benchmark/fs/bench-filehandle-pull-vs-webstream.js"},
	}

	builders := map[string]func() *Grammar{
		"swift":      SwiftGrammar,
		"kotlin":     KotlinGrammar,
		"go":         GoGrammar,
		"typescript": TypeScriptGrammar,
		"javascript": JavaScriptGrammar,
		"json":       JSONGrammar,
		"fortran":    FortranGrammar,
		"jsx":        JSXGrammar,
		"tsx":        TSXGrammar,
		"ts":         TSGrammar,
	}

	snippets := map[string]string{
		"swift": `import Foundation

@available(iOS 13, *)
public actor Counter {
    private var value = 0
    public func increment(by amount: Int = 1) async -> Int {
        value += amount
        return value
    }
}

struct Point: Codable, Hashable {
    var x: Double
    var y: Double
    static let zero = Point(x: 0, y: 0)
}

func classify(_ n: Int) -> String {
    switch n {
    case let x where x < 0: return "negative"
    case 0: return "zero"
    default: return "positive"
    }
}
`,
		"kotlin": `package com.example

data class User(val name: String, val age: Int = 0) {
    fun greet(): String = "Hello, $name!"
}

sealed class Result<out T> {
    data class Success<T>(val value: T) : Result<T>()
    data class Failure(val error: Throwable) : Result<Nothing>()
}

suspend fun fetch(id: Int): Result<User> = try {
    Result.Success(User("user$id", id))
} catch (e: Exception) {
    Result.Failure(e)
}

fun main() {
    val users = listOf(1, 2, 3).map { User("u$it") }
    for (u in users) println(u.greet())
}
`,
		"go": `package sample

import (
	"fmt"
	"strings"
)

type Point struct {
	X, Y int
}

func (p Point) String() string {
	return fmt.Sprintf("(%d, %d)", p.X, p.Y)
}

func Sum(nums ...int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total
}

func main() {
	p := Point{X: 1, Y: 2}
	fmt.Println(strings.ToUpper(p.String()))
}
`,
		"typescript": `interface Shape {
    area(): number;
}

class Circle implements Shape {
    constructor(private radius: number) {}
    area(): number {
        return Math.PI * this.radius ** 2;
    }
}

function describe<T extends Shape>(shape: T): string {
    return ` + "`area=${shape.area().toFixed(2)}`" + `;
}

const shapes: Shape[] = [new Circle(1), new Circle(2)];
export const areas = shapes.map((s) => s.area());
`,
		"javascript": `class Counter {
    #value = 0;
    increment(amount = 1) {
        this.#value += amount;
        return this.#value;
    }
}

const nums = [1, 2, 3].map((n) => n * 2).filter((n) => n > 2);

async function fetchAll(urls) {
    return Promise.all(urls.map((u) => fetch(u).then((r) => r.json())));
}

export { Counter, fetchAll };
`,
		"json": `{
    "name": "example",
    "version": "1.0.0",
    "flags": [true, false, null],
    "nested": {"a": 1, "b": [1, 2, 3.14, -2e10]}
}
`,
		"jsx": `const App = () => (
    <div className="app">
        <Header title="Hello" />
        {items.map((item) => (
            <Item key={item.id} {...item} />
        ))}
    </div>
);
`,
		"tsx": `const App: React.FC<Props> = ({ title }) => (
    <div className="app">
        <h1>{title}</h1>
        {items.map((item: Item) => (
            <Row key={item.id} value={item.value} />
        ))}
    </div>
);
`,
		"ts": `interface Shape {
    area(): number;
}

class Circle implements Shape {
    constructor(private radius: number) {}
    area(): number {
        return Math.PI * this.radius ** 2;
    }
}
`,
	}

	genWith := func(t *testing.T, g *Grammar, disable bool) *gotreesitter.Language {
		t.Helper()
		if disable {
			t.Setenv("GTS_GRAMMARGEN_DISABLE_LEX_MINIMIZE", "1")
		} else {
			t.Setenv("GTS_GRAMMARGEN_DISABLE_LEX_MINIMIZE", "0")
		}
		lang, err := GenerateLanguage(g)
		if err != nil {
			t.Fatalf("generate (disable=%v): %v", disable, err)
		}
		return lang
	}

	parseSExpr := func(t *testing.T, lang *gotreesitter.Language, src []byte) string {
		t.Helper()
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		return tree.RootNode().SExpr(lang)
	}

	byGrammar := make(map[string][]corpusCase)
	for _, cc := range corpusCases {
		byGrammar[cc.grammar] = append(byGrammar[cc.grammar], cc)
	}

	for name, builder := range builders {
		name, builder := name, builder
		t.Run(name, func(t *testing.T) {
			// Build the pre/post Language pair ONCE per grammar and reuse it
			// across every input (snippet + any available corpus files) for
			// that grammar -- generation itself (not parsing) dominates
			// runtime for large grammars like swift/kotlin.
			preLang := genWith(t, builder(), true)
			postLang := genWith(t, builder(), false)

			if len(preLang.LexStates) < len(postLang.LexStates) {
				t.Fatalf("post-fix LexStates (%d) unexpectedly larger than pre-fix (%d)",
					len(postLang.LexStates), len(preLang.LexStates))
			}
			t.Logf("lex_states pre=%d post=%d", len(preLang.LexStates), len(postLang.LexStates))

			check := func(t *testing.T, label string, src []byte) {
				t.Helper()
				preSExpr := parseSExpr(t, preLang, src)
				postSExpr := parseSExpr(t, postLang, src)
				if preSExpr != postSExpr {
					t.Fatalf("%s: parse tree diverged after lex-state minimization\n--- pre (unminimized) ---\n%s\n--- post (minimized) ---\n%s",
						label, preSExpr, postSExpr)
				}
			}

			if snippet, ok := snippets[name]; ok {
				t.Run("snippet", func(t *testing.T) {
					check(t, "snippet", []byte(snippet))
				})
			}

			for _, cc := range byGrammar[name] {
				cc := cc
				t.Run("corpus/"+baseName(cc.path), func(t *testing.T) {
					data, err := os.ReadFile(cc.path)
					if err != nil {
						t.Skipf("corpus file unavailable: %v", err)
					}
					check(t, cc.path, data)
				})
			}
		})
	}
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
