package grammargen

// JSONGrammar returns the JSON grammar defined using the Go DSL.
// This mirrors tree-sitter-json's grammar.js definition.
func JSONGrammar() *Grammar {
	g := NewGrammar("json")

	// document: repeat(_value)
	g.Define("document", Repeat(Sym("_value")))

	// _value: choice(object, array, number, string, true, false, null)
	// The underscore prefix makes this a hidden rule.
	g.Define("_value", Choice(
		Sym("object"),
		Sym("array"),
		Sym("number"),
		Sym("string"),
		Sym("true"),
		Sym("false"),
		Sym("null"),
	))

	// object: seq("{", optional(commaSep(pair)), "}")
	g.Define("object", Seq(
		Str("{"),
		Optional(CommaSep1(Sym("pair"))),
		Str("}"),
	))

	// pair: seq(field("key", string), ":", field("value", _value))
	g.Define("pair", Seq(
		Field("key", Sym("string")),
		Str(":"),
		Field("value", Sym("_value")),
	))

	// array: seq("[", optional(commaSep(_value)), "]")
	g.Define("array", Seq(
		Str("["),
		Optional(CommaSep1(Sym("_value"))),
		Str("]"),
	))

	// string: seq('"', optional(_string_content), '"')
	g.Define("string", Seq(
		Str("\""),
		Optional(Sym("_string_content")),
		Str("\""),
	))

	// _string_content: repeat1(choice(string_content, escape_sequence))
	g.Define("_string_content", Repeat1(Choice(
		Sym("string_content"),
		Sym("escape_sequence"),
	)))

	// string_content: token.immediate(prec(1, /[^\\\"\n]+/))
	g.Define("string_content", ImmToken(Prec(1, Pat(`[^\\\"\n]+`))))

	// escape_sequence: token.immediate(seq("\\", choice(/["\\/bfnrt]/, /u[0-9a-fA-F]{4}/)))
	g.Define("escape_sequence", ImmToken(Seq(
		Str("\\"),
		Choice(
			Pat(`[\"\\\/bfnrt]`),
			Seq(Str("u"), Pat(`[0-9a-fA-F]`), Pat(`[0-9a-fA-F]`), Pat(`[0-9a-fA-F]`), Pat(`[0-9a-fA-F]`)),
		),
	)))

	// number: token(seq(
	//   optional("-"),
	//   choice("0", seq(/[1-9]/, repeat(/[0-9]/))),
	//   optional(seq(".", repeat1(/[0-9]/))),
	//   optional(seq(/[eE]/, optional(/[+-]/), repeat1(/[0-9]/)))
	// ))
	g.Define("number", Token(Seq(
		Optional(Str("-")),
		Choice(
			Str("0"),
			Seq(Pat(`[1-9]`), Repeat(Pat(`[0-9]`))),
		),
		Optional(Seq(Str("."), Repeat1(Pat(`[0-9]`)))),
		Optional(Seq(Pat(`[eE]`), Optional(Pat(`[+\-]`)), Repeat1(Pat(`[0-9]`)))),
	)))

	// true, false, null are string literal rules.
	g.Define("true", Str("true"))
	g.Define("false", Str("false"))
	g.Define("null", Str("null"))

	// comment: token(choice(seq("//", /.*/), seq("/*", /[^*]*\*+([^/*][^*]*\*+)*/, "/")))
	g.Define("comment", Token(Choice(
		Seq(Str("//"), Pat(`.*`)),
		Seq(Str("/*"), Pat(`[^*]*\*+([^/*][^*]*\*+)*`), Str("/")),
	)))

	// Extras: whitespace and comments.
	g.SetExtras(Pat(`\s`), Sym("comment"))

	// _value is a supertype.
	g.SetSupertypes("_value")

	return g
}
