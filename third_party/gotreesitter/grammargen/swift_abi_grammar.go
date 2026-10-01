package grammargen

// SwiftABIManglingGrammar returns a conservative grammar for Swift ABI mangled
// names. It intentionally models ABI symbol text, not Swift source syntax.
func SwiftABIManglingGrammar() *Grammar {
	g := NewGrammar("swift_abi_mangling")
	g.SetExtras(Pat(`[ \t\r\n]+`))

	g.Define("mangled_name", Seq(
		Field("prefix", Sym("prefix")),
		Field("global", Sym("global")),
	))

	g.Define("prefix", Choice(
		Str("$s"),             // Stable Swift mangling.
		Str("@__swiftmacro_"), // Swift macro filename mangling.
		Str("_T0"),            // Swift 4.0.
		Str("$S"),             // Swift 4.2.
		Str("$e"),             // Embedded Swift, currently unstable.
	))

	g.Define("global", Repeat1(Sym("operator")))

	g.Define("operator", Choice(
		Sym("identifier"),
		Sym("symbolic_reference"),
		Sym("operator_code"),
		Sym("index"),
	))

	// Swift mangling identifiers are length-prefixed. This grammar recognizes
	// the token shape; validating that the length matches the payload belongs in
	// a semantic demangling pass.
	g.Define("identifier", Token(Pat(`[0-9]+[A-Za-z_][A-Za-z0-9_]*`)))
	g.Define("index", Token(Pat(`[0-9]+`)))
	g.Define("operator_code", Token(Pat(`[A-Za-z][A-Za-z0-9]*`)))
	g.Define("symbolic_reference", Token(Pat(`[\x01-\x1F][\x00-\xFF]{4,8}`)))

	g.Test("stable nominal type", "$s4Test3FooC", "")
	g.Test("swift 4.2 prefix", "$S4Test3FooC", "")
	g.Test("embedded swift prefix", "$e4Test3FooC", "")
	g.Test("macro filename prefix", "@__swiftmacro_4Test3FooC", "")

	return g
}
