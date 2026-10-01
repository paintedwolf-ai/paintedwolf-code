package theme

// Language-normalized scopes fold to their parents.

// SyntaxScope is one entry in the scope vocabulary.
type SyntaxScope struct {
	// ID is the dotted author-facing scope name.
	ID string
	// Description is one line for the generated schema and docs.
	Description string
	// Parent is the fallback scope; roots use body text.
	Parent string
	// Tags are the highlight expressions this scope paints.
	Tags []string
	// CSSVar is the custom property carrying the scope color.
	CSSVar string
	// StyleOnly emits typography without replacing color.
	StyleOnly bool
}

var syntaxScopes = []SyntaxScope{
	// Roots.
	{ID: "comment", Description: "Comments of every form.", Tags: []string{"comment", "lineComment", "blockComment"}, CSSVar: "--den-code-comment"},
	{ID: "keyword", Description: "Language keywords.", Tags: []string{"keyword"}, CSSVar: "--den-code-keyword"},
	{ID: "string", Description: "String literals.", Tags: []string{"string"}, CSSVar: "--den-code-string"},
	{ID: "number", Description: "Numeric literals.", Tags: []string{"number"}, CSSVar: "--den-code-number"},
	{ID: "constant", Description: "Language constants and atoms.", Tags: []string{"atom"}, CSSVar: "--den-code-constant"},
	{ID: "variable", Description: "Variable names.", Tags: []string{"variableName"}, CSSVar: "--den-code-variable"},
	{ID: "type", Description: "Type names.", Tags: []string{"typeName"}, CSSVar: "--den-code-type"},
	{ID: "function", Description: "Function and method names.", Tags: []string{"function(variableName)"}, CSSVar: "--den-code-function"},
	{ID: "property", Description: "Object and record properties.", Tags: []string{"propertyName"}, CSSVar: "--den-code-property"},
	{ID: "operator", Description: "Operators.", Tags: []string{"operator"}, CSSVar: "--den-code-operator"},
	{ID: "punctuation", Description: "Separators, brackets, and delimiters.", Tags: []string{"punctuation"}, CSSVar: "--den-code-punctuation"},
	{ID: "tag", Description: "Markup tag names.", Tags: []string{"tagName"}, CSSVar: "--den-code-tag"},
	{ID: "meta", Description: "Directives, annotations, and pragmas.", Tags: []string{"meta"}, CSSVar: "--den-code-meta"},
	{ID: "invalid", Description: "Malformed or rejected source.", Tags: []string{"invalid"}, CSSVar: "--den-code-invalid"},
	{ID: "link", Description: "Links and URLs.", Tags: []string{"link", "url"}, CSSVar: "--den-code-link"},
	{ID: "heading", Description: "Markup headings.", Tags: []string{"heading"}, CSSVar: "--den-code-heading"},
	{ID: "inserted", Description: "Added lines in embedded diffs.", Tags: []string{"inserted"}, CSSVar: "--den-code-inserted"},
	{ID: "deleted", Description: "Removed lines in embedded diffs.", Tags: []string{"deleted"}, CSSVar: "--den-code-deleted"},
	{ID: "emphasis", Description: "Emphasized markup text.", Tags: []string{"emphasis"}, CSSVar: "--den-code-emphasis", StyleOnly: true},
	{ID: "strong", Description: "Strong markup text.", Tags: []string{"strong"}, CSSVar: "--den-code-strong", StyleOnly: true},
	{ID: "quote", Description: "Block quotes.", Tags: []string{"quote"}, CSSVar: "--den-code-quote"},
	{ID: "strikethrough", Description: "Struck-through markup text.", Tags: []string{"strikethrough"}, CSSVar: "--den-code-strikethrough", StyleOnly: true},

	// Comment children.
	{ID: "comment.doc", Description: "Documentation comments.", Parent: "comment", Tags: []string{"docComment"}, CSSVar: "--den-code-comment-doc"},

	// Keyword children.
	{ID: "keyword.control", Description: "Control flow (if, for, return).", Parent: "keyword", Tags: []string{"controlKeyword"}, CSSVar: "--den-code-keyword-control"},
	{ID: "keyword.definition", Description: "Definition keywords (func, class, let).", Parent: "keyword", Tags: []string{"definitionKeyword"}, CSSVar: "--den-code-keyword-definition"},
	{ID: "keyword.module", Description: "Import and export keywords.", Parent: "keyword", Tags: []string{"moduleKeyword"}, CSSVar: "--den-code-keyword-module"},
	{ID: "keyword.operator", Description: "Word-shaped operators (in, is, typeof).", Parent: "keyword", Tags: []string{"operatorKeyword"}, CSSVar: "--den-code-keyword-operator"},
	{ID: "keyword.self", Description: "Self and this references.", Parent: "keyword", Tags: []string{"self"}, CSSVar: "--den-code-keyword-self"},

	// String children.
	{ID: "string.special", Description: "Template and interpolated strings.", Parent: "string", Tags: []string{"special(string)"}, CSSVar: "--den-code-string-special"},
	{ID: "string.escape", Description: "Escape sequences inside strings.", Parent: "string", Tags: []string{"escape"}, CSSVar: "--den-code-string-escape"},
	{ID: "string.regexp", Description: "Regular expression literals.", Parent: "string", Tags: []string{"regexp"}, CSSVar: "--den-code-string-regexp"},
	{ID: "string.character", Description: "Character literals.", Parent: "string", Tags: []string{"character"}, CSSVar: "--den-code-string-character"},
	{ID: "string.doc", Description: "Docstrings.", Parent: "string", Tags: []string{"docString"}, CSSVar: "--den-code-string-doc"},

	// Number and constant children.
	{ID: "number.integer", Description: "Integer literals.", Parent: "number", Tags: []string{"integer"}, CSSVar: "--den-code-number-integer"},
	{ID: "number.float", Description: "Floating-point literals.", Parent: "number", Tags: []string{"float"}, CSSVar: "--den-code-number-float"},
	{ID: "constant.boolean", Description: "Boolean literals.", Parent: "constant", Tags: []string{"bool"}, CSSVar: "--den-code-constant-boolean"},
	{ID: "constant.null", Description: "Null and nil literals.", Parent: "constant", Tags: []string{"null"}, CSSVar: "--den-code-constant-null"},
	{ID: "constant.variable", Description: "Constant-valued identifiers.", Parent: "constant", Tags: []string{"constant(variableName)"}, CSSVar: "--den-code-constant-variable"},

	// Variable children.
	{ID: "variable.parameter", Description: "Function parameters.", Parent: "variable", Tags: []string{"local(variableName)"}, CSSVar: "--den-code-variable-parameter"},
	{ID: "variable.definition", Description: "Identifiers at their definition site.", Parent: "variable", Tags: []string{"definition(variableName)"}, CSSVar: "--den-code-variable-definition"},
	{ID: "variable.special", Description: "Language-special identifiers.", Parent: "variable", Tags: []string{"special(variableName)"}, CSSVar: "--den-code-variable-special"},

	// Type, function, property children.
	{ID: "type.class", Description: "Class names.", Parent: "type", Tags: []string{"className"}, CSSVar: "--den-code-type-class"},
	{ID: "type.namespace", Description: "Namespaces and packages.", Parent: "type", Tags: []string{"namespace"}, CSSVar: "--den-code-type-namespace"},
	{ID: "function.definition", Description: "Function names at their definition site.", Parent: "function", Tags: []string{"function(definition(variableName))"}, CSSVar: "--den-code-function-definition"},
	{ID: "function.method", Description: "Method calls on a receiver.", Parent: "function", Tags: []string{"function(propertyName)"}, CSSVar: "--den-code-function-method"},
	{ID: "function.macro", Description: "Macros and labels.", Parent: "function", Tags: []string{"macroName", "labelName"}, CSSVar: "--den-code-function-macro"},
	{ID: "property.definition", Description: "Properties at their definition site.", Parent: "property", Tags: []string{"definition(propertyName)"}, CSSVar: "--den-code-property-definition"},
	{ID: "property.attribute", Description: "Markup attribute names.", Parent: "property", Tags: []string{"attributeName"}, CSSVar: "--den-code-property-attribute"},

	// Operator and punctuation children.
	{ID: "operator.definition", Description: "Assignment and definition operators.", Parent: "operator", Tags: []string{"definitionOperator"}, CSSVar: "--den-code-operator-definition"},
	{ID: "punctuation.bracket", Description: "Brackets, braces, and parentheses.", Parent: "punctuation", Tags: []string{"bracket"}, CSSVar: "--den-code-punctuation-bracket"},
	{ID: "punctuation.separator", Description: "Commas and semicolons.", Parent: "punctuation", Tags: []string{"separator"}, CSSVar: "--den-code-punctuation-separator"},

	// Meta children.
	{ID: "meta.annotation", Description: "Annotations and decorators.", Parent: "meta", Tags: []string{"annotation"}, CSSVar: "--den-code-meta-annotation"},
	{ID: "meta.processing", Description: "Processing instructions.", Parent: "meta", Tags: []string{"processingInstruction"}, CSSVar: "--den-code-meta-processing"},
}

// SyntaxStyle is one resolved scope.
type SyntaxStyle struct {
	Color     Color
	Italic    bool
	Bold      bool
	Underline bool
}

var syntaxByID = func() map[string]SyntaxScope {
	out := make(map[string]SyntaxScope, len(syntaxScopes))
	for _, scope := range syntaxScopes {
		out[scope.ID] = scope
	}
	return out
}()

// SyntaxScopes returns scopes in parent-first order.
func SyntaxScopes() []SyntaxScope {
	return append([]SyntaxScope(nil), syntaxScopes...)
}

// SyntaxScopeByID looks one up.
func SyntaxScopeByID(id string) (SyntaxScope, bool) {
	scope, ok := syntaxByID[id]
	return scope, ok
}
