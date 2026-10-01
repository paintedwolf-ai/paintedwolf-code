package grammargen

// JSGrammar returns the JSX-capable JavaScript grammar.
func JSGrammar() *Grammar {
	return JavaScriptGrammar()
}

// JSXGrammar returns the JSX-capable JavaScript grammar. The upstream lockfile
// does not carry a separate jsx language; JSX is parsed by the JavaScript
// grammar.
func JSXGrammar() *Grammar {
	return JavaScriptGrammar()
}

// JavascriptGrammar is kept for consistency with grammars.JavascriptLanguage.
func JavascriptGrammar() *Grammar {
	return JavaScriptGrammar()
}

// TSGrammar returns the TypeScript grammar.
func TSGrammar() *Grammar {
	return TypeScriptGrammar()
}

// TypescriptGrammar is kept for consistency with grammars.TypescriptLanguage.
func TypescriptGrammar() *Grammar {
	return TypeScriptGrammar()
}

// TsxGrammar is kept for consistency with grammars.TsxLanguage.
func TsxGrammar() *Grammar {
	return TSXGrammar()
}
