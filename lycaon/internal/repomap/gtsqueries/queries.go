package gtsqueries

// tagsQueries supplies declaration captures by grammar.
var tagsQueries = map[string]string{
	// --- Markup / docs ---
	"markdown": `
(atx_heading (inline) @name) @definition.section
(setext_heading (paragraph) @name) @definition.section
`,
	"markdown_inline": `
(latex_block) @definition.section
`,
	"html": `
(start_tag
  (tag_name) @name
  (#match? @name "^h[1-6]$")) @definition.section
`,
	"xml": `
(element (STag (Name) @name)) @definition.tag
`,
	"dtd": `
(elementdecl (Name) @name) @definition.type
`,
	"rst": `
(section (title) @name) @definition.section
`,
	"org": `
(headline (item) @name) @definition.section
`,
	"norg": `
(heading1 (paragraph_segment) @name) @definition.section
(heading2 (paragraph_segment) @name) @definition.section
(heading3 (paragraph_segment) @name) @definition.section
`,
	"djot": `
(heading (content) @name) @definition.section
`,
	"typst": `
(heading) @definition.section
`,
	"vimdoc": `
(heading) @definition.section
(tag (word) @name) @definition.field
`,
	"bibtex": `
(entry (key_brace) @name) @definition.field
`,
	"jsdoc": `
(tag (identifier) @name) @definition.field
`,

	// --- Config / data ---
	"yaml": `
(block_mapping_pair
  key: (flow_node
    (plain_scalar
      (string_scalar) @name))) @definition.field
(block_mapping_pair
  key: (flow_node
    [
      (double_quote_scalar) @name
      (single_quote_scalar) @name
    ])) @definition.field
`,
	"toml": `
(pair
  (bare_key) @name) @definition.field
(table
  (bare_key) @name) @definition.section
`,
	"json": `
(pair
  key: (string (string_content) @name)) @definition.field
`,
	"json5": `
(member (identifier) @name) @definition.field
(member (string) @name) @definition.field
`,
	"jsonnet": `
(field (fieldname) @name) @definition.field
`,
	"ini": `
(section (section_name) @name) @definition.section
(setting (setting_name) @name) @definition.field
`,
	"properties": `
(property (key) @name) @definition.field
`,
	"editorconfig": `
(section (header) @name) @definition.section
(pair (property) @name) @definition.field
`,
	"git_config": `
(section (section_header (section_name) @name)) @definition.section
(variable (name) @name) @definition.field
`,
	"ssh_config": `
(host_declaration (pattern) @name) @definition.section
`,
	"desktop": `
(group (header (group_name) @name)) @definition.section
(entry (identifier) @name) @definition.field
`,
	"hyprlang": `
(section (name) @name) @definition.section
(assignment (name) @name) @definition.field
`,
	"kdl": `
(node (identifier) @name) @definition.field
`,
	"kconfig": `
(config (name) @name) @definition.field
(menuconfig (name) @name) @definition.field
`,
	"corn": `
(pair (path) @name) @definition.field
(assignment (path) @name) @definition.variable
`,
	"cpon": `
(pair (string) @name) @definition.field
`,
	"ron": `
(struct_entry (identifier) @name) @definition.field
(struct (identifier) @name) @definition.type
`,
	"pkl": `
(classProperty (identifier) @name) @definition.field
(classMethod (methodHeader (identifier) @name)) @definition.method
(clazz (identifier) @name) @definition.class
(typeAlias (identifier) @name) @definition.type
`,
	"nickel": `
(field_decl (field_def (field_path (field_path_elem (ident) @name)))) @definition.field
`,
	"cue": `
(field (label) @name) @definition.field
`,
	"hurl": `
(entry (request (method) @name)) @definition.field
`,
	"http": `
(request (method) @name) @definition.field
(variable_declaration (identifier) @name) @definition.constant
`,
	"foam": `
(dict (identifier) @name) @definition.section
(key_value (identifier) @name) @definition.field
`,
	"textproto": `
(field (scalar_field (field_name) @name)) @definition.field
(field (message_field (field_name) @name)) @definition.field
`,
	"csv": `
(field) @definition.field
`,
	"pem": `
(header (label) @name) @definition.field
`,

	// --- Build / infra ---
	"dockerfile": `
(from_instruction
  (image_spec
    (image_name) @name)) @definition.module
`,
	"earthfile": `
(target (identifier) @name) @definition.function
(function_command (identifier) @name) @definition.function
`,
	"hcl": `
(block
  (identifier) @name) @definition.section
`,
	"cmake": `
(function_def (function_command (function) (argument_list (argument) @name))) @definition.function
(macro_def (macro_command (macro) (argument_list (argument) @name))) @definition.function
`,
	"make": `
(rule (targets (word) @name)) @definition.target
(variable_assignment (word) @name) @definition.variable
(define_directive (word) @name) @definition.function
`,
	"meson": `
(operatorunit (identifier) @name) @definition.variable
`,
	"ninja": `
(rule (identifier) @name) @definition.function
`,
	"just": `
(recipe (recipe_header (identifier) @name)) @definition.function
(assignment (identifier) @name) @definition.variable
(module (identifier) @name) @definition.module
`,
	"nginx": `
(directive (keyword) @name) @definition.field
`,
	"caddy": `
(snippet (snippet_name) @name) @definition.function
`,
	"gomod": `
(module_directive (module_path) @name) @definition.module
`,
	"linkerscript": `
(output_section (symbol) @name) @definition.section
(assignment (symbol) @name) @definition.variable
`,

	// --- Shell / scripting ---
	"bash": `
(function_definition
  name: (word) @name) @definition.function
(variable_assignment
  name: (variable_name) @name) @definition.variable
`,
	"fish": `
(function_definition (word) @name) @definition.function
`,
	"awk": `
(func_def (identifier) @name) @definition.function
`,
	"nushell": `
(decl_def (cmd_identifier) @name) @definition.function
(decl_module (cmd_identifier) @name) @definition.module
`,
	"powershell": `
(function_statement (function_name) @name) @definition.function
(class_statement (type_identifier) @name) @definition.class
(class_method_definition (function_name) @name) @definition.method
(enum_statement (type_identifier) @name) @definition.type
`,
	"tcl": `
(procedure (simple_word) @name) @definition.function
`,
	"perl": `
(subroutine_declaration_statement (bareword) @name) @definition.function
(package_statement (package) @name) @definition.module
(method_declaration_statement (bareword) @name) @definition.method
`,
	"groovy": `
(function_definition function: (identifier) @name) @definition.function
(function_declaration function: (identifier) @name) @definition.function
(class_definition name: (identifier) @name) @definition.class
`,
	"php": `
(function_definition (name) @name) @definition.function
(method_declaration (name) @name) @definition.method
(class_declaration (name) @name) @definition.class
(interface_declaration (name) @name) @definition.interface
(trait_declaration (name) @name) @definition.type
(enum_declaration (name) @name) @definition.type
`,
	"twig": `
(macro_statement (method) @name) @definition.function
(assignment_statement (variable) @name) @definition.variable
`,
	"liquid": `
(assignment_statement (identifier) @name) @definition.variable
(capture_statement (identifier) @name) @definition.variable
`,

	// --- Functional ---
	"haskell": `
(function (variable) @name) @definition.function
(signature (variable) @name) @definition.function
(data_type (name) @name) @definition.type
(class (name) @name) @definition.class
(newtype (name) @name) @definition.type
(type_synomym (name) @name) @definition.type
`,
	"ocaml": `
(value_definition (let_binding (value_name) @name)) @definition.function
(type_definition (type_binding (type_constructor) @name)) @definition.type
(module_definition (module_binding (module_name) @name)) @definition.module
(class_definition (class_binding (class_name) @name)) @definition.class
(method_definition (method_name) @name) @definition.method
`,
	"elm": `
(value_declaration (function_declaration_left (lower_case_identifier) @name)) @definition.function
(type_declaration (upper_case_identifier) @name) @definition.type
(type_alias_declaration (upper_case_identifier) @name) @definition.type
(module_declaration (upper_case_qid (upper_case_identifier) @name)) @definition.module
`,
	"purescript": `
(function (variable) @name) @definition.function
(signature (variable) @name) @definition.function
(type_alias (type_name) @name) @definition.type
(data (type_name) @name) @definition.type
(newtype (type_name) @name) @definition.type
(class_declaration (class_head (class_name) @name)) @definition.class
`,
	"fennel": `
[(local_form (symbol) @name)
 (var_form (symbol) @name)
 (global_form (symbol) @name)] @definition.variable
[(fn_form (symbol) @name)
 (lambda_form (symbol) @name)
 (macro_form (symbol) @name)] @definition.function
`,
	"clojure": `
(list_lit
  (sym_lit) @_def
  (sym_lit) @name
  (#match? @_def "^(def|defn|defn-|defmacro|defmulti|defprotocol|defrecord|deftype|ns)$")) @definition.function
`,
	"commonlisp": `
(defun (defun_header (sym_lit) @name)) @definition.function
`,
	"elisp": `
(function_definition (symbol) @name) @definition.function
(macro_definition (symbol) @name) @definition.function
`,
	"janet": `
(par_tup_lit
  (sym_lit) @_def
  (sym_lit) @name
  (#match? @_def "^(def|defn|defn-|def-|defmacro|defglobal|varfn|var)$")) @definition.function
`,
	"erlang": `
(function_clause (atom) @name) @definition.function
(module_attribute (atom) @name) @definition.module
(type_alias (type_name (atom) @name)) @definition.type
(record_decl (atom) @name) @definition.type
`,

	// --- Systems / compiled ---
	"forth": `
(word_definition (word) @name) @definition.function
`,
	"pascal": `
(defProc (identifier) @name) @definition.function
(declProc (identifier) @name) @definition.function
(declClass (identifier) @name) @definition.class
(declType (identifier) @name) @definition.type
`,
	"cobol": `
(program_definition (identification_division (program_name) @name)) @definition.module
(paragraph_header) @definition.function
(section_header) @definition.section
`,
	"brightscript": `
(function_statement (identifier) @name) @definition.function
(sub_statement (identifier) @name) @definition.function
`,
	"firrtl": `
(module (identifier) @name) @definition.module
(extmodule (identifier) @name) @definition.module
`,
	"verilog": `
(module_declaration (module_header (simple_identifier) @name)) @definition.module
(function_declaration (function_body_declaration (function_identifier) @name)) @definition.function
(task_declaration (task_body_declaration (task_identifier) @name)) @definition.function
(class_declaration (class_identifier) @name) @definition.class
`,
	"vhdl": `
(entity_declaration (identifier) @name) @definition.type
(architecture_definition (identifier) @name) @definition.module
(package_declaration (identifier) @name) @definition.module
(type_declaration (identifier) @name) @definition.type
`,
	"wat": `
(module_field_func (identifier) @name) @definition.function
(module_field_global (identifier) @name) @definition.variable
(module_field_type (identifier) @name) @definition.type
`,
	"llvm": `
(fn_define (function_header (global_var) @name)) @definition.function
`,
	"asm": `
(label (ident) @name) @definition.label
`,
	"disassembly": `
(source_location (code_location (identifier) @name)) @definition.function
`,
	"tablegen": `
(def (value (identifier) @name)) @definition.type
(class (class_identifier) @name) @definition.class
(multiclass (identifier) @name) @definition.class
`,
	"uxntal": `
(macro (identifier) @name) @definition.function
`,
	"nim": `
(proc_declaration (exported_symbol (identifier) @name)) @definition.function
(proc_declaration (identifier) @name) @definition.function
(func_declaration (identifier) @name) @definition.function
(method_declaration (identifier) @name) @definition.method
(template_declaration (identifier) @name) @definition.function
(macro_declaration (identifier) @name) @definition.function
(iterator_declaration (identifier) @name) @definition.function
`,

	// --- Interface / schema ---
	"graphql": `
(object_type_definition (name) @name) @definition.type
(interface_type_definition (name) @name) @definition.interface
(enum_type_definition (name) @name) @definition.type
(input_object_type_definition (name) @name) @definition.type
(scalar_type_definition (name) @name) @definition.type
(union_type_definition (name) @name) @definition.type
(field_definition (name) @name) @definition.field
(directive_definition (name) @name) @definition.function
(fragment_definition (fragment_name) @name) @definition.type
`,
	"proto": `
(message (message_name) @name) @definition.type
(enum (enum_name) @name) @definition.type
(service (service_name) @name) @definition.interface
(rpc (rpc_name) @name) @definition.method
(field (identifier) @name) @definition.field
`,
	"smithy": `
(shape_statement (identifier) @name) @definition.type
(simple_shape_statement (identifier) @name) @definition.type
(structure_statement (identifier) @name) @definition.type
(service_statement (identifier) @name) @definition.interface
(operation_statement (identifier) @name) @definition.method
(enum_statement (identifier) @name) @definition.type
`,
	"fidl": `
(layout_declaration (identifier) @name) @definition.type
(protocol_declaration (identifier) @name) @definition.interface
(const_declaration (identifier) @name) @definition.constant
(service_declaration (identifier) @name) @definition.interface
(type_alias_declaration (identifier) @name) @definition.type
`,
	"facility": `
(service (identifier) @name) @definition.interface
(method (identifier) @name) @definition.method
(enum (identifier) @name) @definition.type
(field (identifier) @name) @definition.field
`,
	"ebnf": `
(syntax_rule (identifier) @name) @definition.type
`,
	"sparql": `
(prefix_declaration (namespace) @name) @definition.module
`,
	"turtle": `
(prefix_id (namespace) @name) @definition.module
`,
	"promql": `
(metric_name) @name @definition.field
`,
	"rego": `
(rule (rule_head (var) @name)) @definition.function
(package (term) @name) @definition.module
`,
	"ql": `
(classlessPredicate (predicateName) @name) @definition.function
(dataclass (className) @name) @definition.class
(module (moduleName) @name) @definition.module
`,
	"sql": `
(create_function_statement (identifier) @name) @definition.function
(create_table_statement (identifier) @name) @definition.type
(create_view_statement (identifier) @name) @definition.type
(create_type_statement (identifier) @name) @definition.type
`,

	// --- Web / templating ---
	"svelte": `
(snippet_statement (snippet_start (snippet_name) @name)) @definition.function
`,
	"vue": `
(start_tag (tag_name) @name) @definition.tag
`,
	"astro": `
(element (start_tag (tag_name) @name)) @definition.tag
`,
	"blade": `
(section (parameter) @name) @definition.section
`,
	"heex": `
(tag (start_tag (tag_name) @name)) @definition.tag
`,
	"css": `
(class_name) @name @definition.class
(id_name) @name @definition.class
(keyframes_statement
  (keyframes_name) @name) @definition.function
`,
	"scss": `
(class_name) @name @definition.class
(mixin_statement
  name: (identifier) @name) @definition.function
(function_statement
  name: (identifier) @name) @definition.function
`,
	"less": `
(mixin_def (class_selector (class_name) @name)) @definition.function
(class_selector (class_name) @name) @definition.class
`,

	// --- General purpose ---
	"c": `
(function_definition
  declarator: (function_declarator declarator: (identifier) @name)) @definition.function
(struct_specifier name: (type_identifier) @name) @definition.type
(union_specifier name: (type_identifier) @name) @definition.type
(enum_specifier name: (type_identifier) @name) @definition.type
(type_definition declarator: (type_identifier) @name) @definition.type
(declaration
  (type_qualifier) @_qual
  declarator: (init_declarator declarator: (identifier) @name)
  (#eq? @_qual "const")) @definition.constant
`,
	"cpp": `
(function_definition
  declarator: (function_declarator
    declarator: [(identifier) (field_identifier)] @name)) @definition.function
(class_specifier name: (type_identifier) @name) @definition.class
(struct_specifier name: (type_identifier) @name) @definition.type
(union_specifier name: (type_identifier) @name) @definition.type
(enum_specifier name: (type_identifier) @name) @definition.type
(namespace_definition name: (namespace_identifier) @name) @definition.module
(alias_declaration name: (type_identifier) @name) @definition.type
(declaration
  (type_qualifier) @_qual
  declarator: (init_declarator declarator: (identifier) @name)
  (#eq? @_qual "const")) @definition.constant
`,
	"go": `
(function_declaration name: (identifier) @name) @definition.function
(method_declaration name: (field_identifier) @name) @definition.method
(type_declaration
  (type_spec name: (type_identifier) @name)) @definition.type
(const_spec name: (identifier) @name) @definition.constant
(var_spec name: (identifier) @name) @definition.variable
`,
	"typescript": `
(function_declaration name: (identifier) @name) @definition.function
(generator_function_declaration name: (identifier) @name) @definition.function
(class_declaration name: (type_identifier) @name) @definition.class
(method_definition name: (property_identifier) @name) @definition.method
(type_alias_declaration name: (type_identifier) @name) @definition.type
(interface_declaration name: (type_identifier) @name) @definition.interface
(enum_declaration name: (identifier) @name) @definition.type
(lexical_declaration
  "const"
  (variable_declarator name: (identifier) @name)) @definition.constant
`,
	"swift": `
(function_declaration name: (simple_identifier) @name) @definition.function
(protocol_function_declaration name: (simple_identifier) @name) @definition.method
(class_declaration declaration_kind: ["class" "actor"] name: (type_identifier) @name) @definition.class
(class_declaration declaration_kind: ["struct" "enum"] name: (type_identifier) @name) @definition.type
(protocol_declaration name: (type_identifier) @name) @definition.interface
(typealias_declaration name: (type_identifier) @name) @definition.type
`,
	"ruby": `
(class name: (constant) @name) @definition.class
(module name: (constant) @name) @definition.module
(method name: (identifier) @name) @definition.method
(singleton_method name: (identifier) @name) @definition.method
`,
	"elixir": `
(call
  target: (identifier) @_def
  (arguments (alias) @name)
  (#match? @_def "^(defmodule|defprotocol|defimpl)$")) @definition.module
(call
  target: (identifier) @_def
  (arguments (call target: (identifier) @name))
  (#match? @_def "^(def|defp|defmacro|defmacrop|defguard|defguardp)$")) @definition.function
(call
  target: (identifier) @_def
  (arguments (identifier) @name)
  (#match? @_def "^(def|defp|defmacro|defmacrop|defguard|defguardp)$")) @definition.function
`,
	"julia": `
(function_definition (signature (call_expression (identifier) @name))) @definition.function
(assignment (call_expression (identifier) @name)) @definition.function
(struct_definition (type_head (identifier) @name)) @definition.type
`,
	"r": `
(binary_operator
  lhs: (identifier) @name
  rhs: (function_definition)) @definition.function
`,

	// --- Misc DSLs ---
	"circom": `
(template_definition name: (identifier) @name) @definition.class
(function_definition name: (identifier) @name) @definition.function
`,
	"nix": `
(binding (attrpath (identifier) @name)) @definition.field
`,
	"dhall": `
(let_binding (label) @name) @definition.variable
`,
	"godot_resource": `
(section (identifier) @name) @definition.section
`,
	"cylc": `
(top_section (section_name) @name) @definition.section
(task_section (task_name) @name) @definition.function
`,
	"beancount": `
(open (account) @name) @definition.field
(commodity (currency) @name) @definition.type
`,
	"chatito": `
(intent_def (intent) @name) @definition.type
(slot_def (slot) @name) @definition.type
(alias_def (alias) @name) @definition.type
`,
	"robot": `
(keyword_definition (name) @name) @definition.function
(test_case_definition (name) @name) @definition.function
(variable_definition (variable_name) @name) @definition.variable
`,
	"mermaid": `
(class_name) @name @definition.class
`,
	"tmux": `
(bind_key_directive (key) @name) @definition.field
`,
	"todotxt": `
(task) @definition.field
`,
	"enforce": `
(decl_class (identifier) @name) @definition.class
(decl_method (identifier) @name) @definition.method
(decl_field (identifier) @name) @definition.field
(decl_variable (identifier) @name) @definition.variable
`,
	"eds": `
(section (section_name) @name) @definition.section
`,
	"agda": `
(function (lhs (function_name) @name)) @definition.function
(module (module_name) @name) @definition.module
`,
	"elsa": `
(definition (function) @name) @definition.function
`,
	"regex": `
(named_capturing_group (group_name) @name) @definition.field
`,
	"diff": `
(new_file (filename) @name) @definition.field
`,
	"comment": `
(tag (name) @name) @definition.field
`,
	"gitattributes": `
(pattern) @name @definition.field
`,
	"dot": `
(source_file (id (identifier) @name)) @definition.module
`,
	"ada": `
(subprogram_body (procedure_specification (identifier) @name)) @definition.function
(subprogram_body (function_specification (identifier) @name)) @definition.function
(subprogram_declaration (procedure_specification (identifier) @name)) @definition.function
(subprogram_declaration (function_specification (identifier) @name)) @definition.function
(package_declaration (identifier) @name) @definition.module
`,
	"bass": `
(list
  (symbol) @_def
  (symbol) @name
  (#match? @_def "^(def|defn|defop|defattrs|defmacro)$")) @definition.function
`,
	"cooklang": `
(ingredient (name) @name) @definition.field
`,
	"doxygen": `
(tag (identifier) @name) @definition.field
`,
	"git_rebase": `
(operation (label) @name) @definition.field
`,
	"gitcommit": `
(subject) @name @definition.field
`,
	"gitignore": `
(pattern) @name @definition.field
`,
	"ledger": `
(posting (account) @name) @definition.field
(plain_xact (payee) @name) @definition.field
`,
	"prolog": `
(clause_term (compound_term (functional_notation (atom) @name))) @definition.function
(clause_term (compound_term (infix_operator (compound_term (functional_notation (atom) @name)) (operator)))) @definition.function
`,
	"racket": `
(list
  .
  (symbol) @_def
  (list (symbol) @name)
  (#match? @_def "^define$")) @definition.function
(list
  .
  (symbol) @_def
  (symbol) @name
  (#match? @_def "^(define|define-syntax|struct)$")) @definition.variable
`,
	"scheme": `
(list
  .
  (symbol) @_def
  (list (symbol) @name)
  (#match? @_def "^define$")) @definition.function
(list
  .
  (symbol) @_def
  (symbol) @name
  (#match? @_def "^(define|define-syntax)$")) @definition.variable
`,
	"requirements": `
(requirement (package) @name) @definition.field
`,
	"yuck": `
(list
  .
  (symbol) @_def
  (symbol) @name
  (#match? @_def "^(defwidget|defvar|defpoll|deflisten|defwindow)$")) @definition.function
`,
}
