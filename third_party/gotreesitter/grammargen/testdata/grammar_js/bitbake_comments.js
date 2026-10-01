// Reduced from tree-sitter-bitbake at
// a5d04fdb5a69a02b8fa8eb5525a60dfb5309b73b.
module.exports = grammar({
  name: 'bitbake',

  externals: $ => [
    $._concat,

    $._newline,
    $._indent,
    $._dedent,
    $.string_start,
    $._string_content,
    $.escape_interpolation,
    $.string_end,

    // Mark comments as external tokens so that the external scanner is always
    // invoked, even if no external token is expected. This allows for better
    // error recovery, because the external scanner can maintain the overall
    // structure by returning dedent tokens whenever a dedent occurs, even
    // if no dedent is expected.
    $.comment,

    // Allow the external scanner to check for the validity of closing brackets
    // so that it can avoid returning dedent tokens between brackets.
    ']',
    ')',
    '}',

    $.shell_content,
  ],

  extras: $ => [
    /\s/,
    $.comment,
    $.line_continuation,
  ],

  rules: {
    recipe: $ => repeat(seq(
      choice(
        $.variable_assignment,
        $.unset_statement,
      ),
      choice(/\r?\n/, '\0'),
    )),

    variable_assignment: $ => 'assignment',
    unset_statement: $ => 'unset',
    comment: $ => /#[^\n]*/,
    line_continuation: $ => '\\\n',
  },
});
