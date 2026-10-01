//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammargen"
)

func main() {
	js.Global().Set("gotreesitter", js.ValueOf(map[string]interface{}{
		"importGrammar":    js.FuncOf(importGrammar),
		"generateLanguage": js.FuncOf(generateLanguage),
		"parse":            js.FuncOf(parse),
		"highlight":        js.FuncOf(highlight),
		"highlightQueries": js.FuncOf(highlightQueries),
		"version":          js.ValueOf("0.1.0"),
	}))

	// Keep alive
	select {}
}

// Cache for generated languages
var languageCache = map[string]*gotreesitter.Language{}
var grammarCache = map[string]*grammargen.Grammar{}
var highlightCache = map[string]string{}

// importGrammar(jsonString) -> {ok: bool, error: string, name: string}
func importGrammar(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return errorResult("missing grammar JSON argument")
	}
	data := []byte(args[0].String())

	g, err := grammargen.ImportGrammarJSON(data)
	if err != nil {
		return errorResult(err.Error())
	}

	grammarCache[g.Name] = g
	return map[string]interface{}{
		"ok":   true,
		"name": g.Name,
	}
}

// generateLanguage(name) -> {ok: bool, error: string}
func generateLanguage(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return errorResult("missing grammar name argument")
	}
	name := args[0].String()

	g, ok := grammarCache[name]
	if !ok {
		return errorResult("grammar not loaded: " + name)
	}

	lang, err := grammargen.GenerateLanguage(g)
	if err != nil {
		return errorResult(err.Error())
	}

	languageCache[name] = lang

	// Auto-generate highlight queries if base Go grammar is available
	if goGrammar := grammarCache["go"]; goGrammar != nil && g.Name != "go" {
		highlightCache[name] = grammargen.GenerateHighlightQueries(goGrammar, g)
	}

	return map[string]interface{}{
		"ok": true,
	}
}

// parse(grammarName, source) -> {ok: bool, error: string, sexp: string, hasError: bool}
func parse(this js.Value, args []js.Value) interface{} {
	if len(args) < 2 {
		return errorResult("usage: parse(grammarName, source)")
	}
	name := args[0].String()
	source := []byte(args[1].String())

	lang, ok := languageCache[name]
	if !ok {
		return errorResult("language not generated: " + name)
	}

	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return errorResult(err.Error())
	}
	if tree == nil {
		return errorResult("parse returned no tree")
	}
	defer tree.Release()

	root := tree.RootNode()
	sexp := ""
	hasError := false
	if root != nil {
		sexp = root.SExpr(lang)
		hasError = root.HasError()
	}
	return map[string]interface{}{
		"ok":       true,
		"sexp":     sexp,
		"hasError": hasError,
	}
}

// highlight(grammarName, source, highlightQuery?) -> {ok: bool, error: string, ranges: [...]}
func highlight(this js.Value, args []js.Value) interface{} {
	if len(args) < 2 {
		return errorResult("usage: highlight(grammarName, source, [highlightQuery])")
	}
	name := args[0].String()
	source := []byte(args[1].String())

	lang, ok := languageCache[name]
	if !ok {
		return errorResult("language not generated: " + name)
	}

	// Use provided query or cached auto-generated query
	query := ""
	if len(args) >= 3 && args[2].Type() == js.TypeString {
		query = args[2].String()
	} else if cached, ok := highlightCache[name]; ok {
		query = cached
	}

	if query == "" {
		return errorResult("no highlight query available for: " + name)
	}

	hl, err := gotreesitter.NewHighlighter(lang, query)
	if err != nil {
		return errorResult("highlighter init: " + err.Error())
	}

	ranges := hl.Highlight(source)

	// Convert to JS-friendly format
	jsRanges := make([]interface{}, len(ranges))
	for i, r := range ranges {
		jsRanges[i] = map[string]interface{}{
			"startByte": r.StartByte,
			"endByte":   r.EndByte,
			"capture":   r.Capture,
		}
	}

	return map[string]interface{}{
		"ok":     true,
		"ranges": jsRanges,
	}
}

// highlightQueries(grammarName) -> {ok: bool, query: string}
func highlightQueries(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return errorResult("missing grammar name")
	}
	name := args[0].String()

	if q, ok := highlightCache[name]; ok {
		return map[string]interface{}{"ok": true, "query": q}
	}
	return errorResult("no highlight query for: " + name)
}

func errorResult(msg string) interface{} {
	return map[string]interface{}{
		"ok":    false,
		"error": msg,
	}
}
