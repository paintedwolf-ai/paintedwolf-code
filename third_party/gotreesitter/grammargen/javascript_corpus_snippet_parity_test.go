package grammargen

import "testing"

func TestJavaScriptCorpusSnippetParity(t *testing.T) {
	if raceEnabled {
		t.Skip("skip heavyweight JavaScript parity generation under -race; non-race coverage keeps the generated-vs-reference check")
	}

	genLang, refLang := loadImportedParityLanguages(t, "javascript")
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "import_attributes_with_clause",
			src:  "import pkg from \"./package.json\" with { type: \"json\" };\n",
		},
		{
			name: "jsx_in_javascript_corpus",
			src:  "var a = <Foo></Foo>\nb = <Foo.Bar></Foo.Bar>\n",
		},
		{
			name: "jsx_corpus_block_exact",
			src: "var a = <Foo></Foo>\n" +
				"b = <Foo.Bar></Foo.Bar>\n" +
				"c = <> <Foo /> </>\n" +
				"d = <Bar> <Foo /> </Bar>\n" +
				"e = <Foo bar/>\n" +
				"f = <Foo bar=\"string\" baz={2} data-i8n=\"dialogs.welcome.heading\" bam />\n" +
				"g = <Avatar userId={foo.creatorId} />\n" +
				"h = <input checked={this.state.selectedNewStreetType === 'new-street-default' || !this.state.selectedNewStreetType}> </input>\n" +
				"i = <Foo:Bar bar={}>{...children}</Foo:Bar>\n",
		},
		{
			name: "jsx_self_closing_multiple_attributes",
			src:  "f = <Foo bar=\"string\" baz={2} data-i8n=\"dialogs.welcome.heading\" bam />\n",
		},
		{
			name: "jsx_self_closing_string_attribute",
			src:  "f = <Foo bar=\"string\" />\n",
		},
		{
			name: "jsx_self_closing_expression_attribute",
			src:  "f = <Foo baz={2} />\n",
		},
		{
			name: "jsx_self_closing_hyphen_string_attribute",
			src:  "f = <Foo data-i8n=\"dialogs.welcome.heading\" />\n",
		},
		{
			name: "jsx_self_closing_multiple_bare_attributes",
			src:  "f = <Foo bar baz bam />\n",
		},
		{
			name: "jsx_self_closing_member_expression_attribute",
			src:  "g = <Avatar userId={foo.creatorId} />\n",
		},
		{
			name: "jsx_logical_expression_attribute_with_children",
			src:  "h = <input checked={this.state.selectedNewStreetType === 'new-street-default' || !this.state.selectedNewStreetType}> </input>\n",
		},
		{
			name: "jsx_namespace_name_empty_attribute_expression",
			src:  "i = <Foo:Bar bar={}>{...children}</Foo:Bar>\n",
		},
		{
			name: "jsx_empty_fragment_corpus_block_exact",
			src:  "a = <></>;\na = <E><></></E>;\n",
		},
		{
			name: "jsx_named_element_with_semicolon",
			src:  "a = <Foo></Foo>;\n",
		},
		{
			name: "jsx_empty_fragment_without_semicolon",
			src:  "a = <></>\n",
		},
		{
			name: "template_strings_from_corpus",
			src:  "`one line`;\n`multi\\n  line`;\n`$${'$'}$$${'$'}$$$$`;\n",
		},
		{
			name: "postfix_update_after_logical_or",
			src:  "x = fn.guid || jQuery.guid++;\n",
		},
		{
			name: "template_strings_corpus_block_exact",
			src: "`one line`;\n" +
				"`multi\n" +
				"  line`;\n\n" +
				"`multi\n" +
				"  ${2 + 2}\n" +
				"  hello\n" +
				"  ${1 + 1, 2 + 2}\n" +
				"  line`;\n\n" +
				"`$$$$`;\n" +
				"`$$$$${ 1 }`;\n\n" +
				"`(a|b)$`;\n\n" +
				"`$`;\n\n" +
				"`$${'$'}$$${'$'}$$$$`;\n\n" +
				"`\\ `;\n\n" +
				"`The command \\`git ${args.join(' ')}\\` exited with an unexpected code: ${exitCode}. The caller should either handle this error, or expect that exit code.`;\n\n" +
				"`\\\\`;\n\n" +
				"`//`;\n",
		},
		{
			name: "function_expression_array_corpus_block_exact",
			src: "[\n" +
				"  function() {},\n" +
				"  function(arg1, ...arg2) {\n" +
				"    arg2;\n" +
				"  },\n" +
				"  function stuff() {},\n" +
				"  function trailing(a,) {},\n" +
				"  function trailing(a,b,) {},\n" +
				"  function reserved(async) {},\n" +
				"  function rest(...[_ = x]) {}\n" +
				"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGeneratedAndReferenceParity(t, genLang, refLang, tt.src)
		})
	}
}
