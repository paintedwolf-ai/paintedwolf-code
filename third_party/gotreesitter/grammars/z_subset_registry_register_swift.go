//go:build grammar_subset && grammar_subset_swift

package grammars

func init() {
	Register(LangEntry{
		Name:          "swift",
		Extensions:    []string{".swift"},
		Language:      SwiftLanguage,
		GrammarSource: GrammarSourceGrammargenBlob,
		HighlightQuery: `
["." ";" ":" ","] @punctuation.delimiter
["(" ")" "[" "]" "{" "}"] @punctuation.bracket

(type_identifier) @type

[(self_expression) (super_expression)] @variable.builtin

["func" "deinit"] @keyword

[
  (visibility_modifier)
  (member_modifier)
  (function_modifier)
  (property_modifier)
  (parameter_modifier)
  (inheritance_modifier)
  (mutation_modifier)
] @keyword

(simple_identifier) @variable

(function_declaration (simple_identifier) @function)
(protocol_function_declaration name: (simple_identifier) @function)
(init_declaration "init" @constructor)

(parameter external_name: (simple_identifier) @variable.parameter)
(parameter name: (simple_identifier) @variable.parameter)
(type_parameter (type_identifier) @variable.parameter)

(inheritance_constraint (identifier (simple_identifier) @variable.parameter))
(equality_constraint (identifier (simple_identifier) @variable.parameter))

[
  "protocol" "extension" "indirect" "nonisolated" "override"
  "convenience" "required" "some" "any" "weak" "unowned"
  "didSet" "willSet" "subscript" "let" "var"
  (throws) (where_keyword) (getter_specifier) (setter_specifier)
  (modify_specifier) (else) (as_operator)
] @keyword

["enum" "struct" "class" "typealias"] @keyword
["async" "await"] @keyword

(shebang_line) @keyword

(class_body (property_declaration (pattern (simple_identifier) @property)))
(protocol_property_declaration (pattern (simple_identifier) @property))
(navigation_expression (navigation_suffix (simple_identifier) @property))
(value_argument name: (value_argument_label (simple_identifier) @property))

(import_declaration "import" @keyword)
(enum_entry "case" @keyword)

(modifiers (attribute "@" @attribute (user_type (type_identifier) @attribute)))

(call_expression (simple_identifier) @function)
(call_expression (navigation_expression (navigation_suffix (simple_identifier) @function)))
(call_expression (prefix_expression (simple_identifier) @function))

((navigation_expression (simple_identifier) @type)
  (#match? @type "^[A-Z]"))

(directive) @keyword

[
  (diagnostic) (availability_condition) (playground_literal)
  (key_path_string_expression) (selector_expression)
  (external_macro_definition)
] @function

(special_literal) @constant.builtin

(for_statement "for" @keyword)
(for_statement "in" @keyword)
["while" "repeat" "continue" "break"] @keyword
(guard_statement "guard" @keyword)
(if_statement "if" @keyword)
(switch_statement "switch" @keyword)
(switch_entry "case" @keyword)
(switch_entry "fallthrough" @keyword)
(switch_entry (default_keyword) @keyword)
"return" @keyword

(ternary_expression ["?" ":"] @keyword)

[(try_operator) "do" (throw_keyword) (catch_keyword)] @keyword

(statement_label) @label

[(comment) (multiline_comment)] @comment

((comment) @comment
  (#match? @comment "^///[^/]"))

((comment) @comment
  (#match? @comment "^///$"))

((multiline_comment) @comment
  (#match? @comment "^/[*][*][^*].*[*]/$"))

(line_str_text) @string
(str_escaped_char) @string.escape
(multi_line_str_text) @string
(raw_str_part) @string
(raw_str_end_part) @string
(line_string_literal ["\\(" ")"] @punctuation)
(multi_line_string_literal ["\\(" ")"] @punctuation)
(raw_str_interpolation [(raw_str_interpolation_start) ")"] @punctuation)
["\"" "\"\"\""] @string

(lambda_literal "in" @keyword)

[(integer_literal) (hex_literal) (oct_literal) (bin_literal)] @number
(real_literal) @number
(boolean_literal) @constant.builtin
"nil" @constant.builtin
(wildcard_pattern) @variable
(regex_literal) @string.regex

(custom_operator) @operator
(bang) @operator
[
  "+" "-" "*" "/" "%" "=" "+=" "-=" "*=" "/="
  "<" ">" "<<" ">>" "<=" ">=" "++" "--"
  "^" "&" "&&" "|" "||" "~" "%="
  "!=" "!==" "==" "===" "?" "??" "->" "..<" "..."
] @operator

(type_arguments ["<" ">"] @punctuation.bracket)
`,
	})
}
