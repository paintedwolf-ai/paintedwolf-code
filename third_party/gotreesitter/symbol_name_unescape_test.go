package gotreesitter

import "testing"

// unescapePunctuationSymbolName must be the exact inverse of grammargen's
// escapeAnonymousName (which escapes only `?` → `\?`). Every other
// backslash in a blob symbol name is literal token text or regex source
// that the C oracle reports verbatim. Cases below are real symbol names
// from the embedded grammar blobs, verified against the pinned C parsers.
func TestUnescapePunctuationSymbolName(t *testing.T) {
	cases := []struct {
		in, want string
		grammar  string
	}{
		// generator-escaped `?` — must unescape
		{`\?`, `?`, "ruby/yaml/many"},
		{`defined\?`, `defined?`, "ruby"},
		{`\?\?=`, `??=`, "javascript/c_sharp"},
		{`\?.`, `?.`, "kotlin/groovy"},
		{`as\?`, `as?`, "kotlin"},
		{`.\?`, `.?`, "matlab/zig"},
		{`(\?<`, `(?<`, "regex"},
		{`\?=`, `?=`, "make/vhdl"},
		{`==\?`, `==?`, "verilog"},
		// literal `\?` token text: generator escapes it to `\\?`, and the
		// inverse must restore exactly one backslash (C displays `\?`).
		{`\\?`, `\?`, "textproto"},
		// literal backslash token text — must stay untouched
		{`\(`, `\(`, "pkl/swift/jq string interpolation"},
		{`\#(`, `\#(`, "pkl/cue custom-delimiter interpolation"},
		{`\######(`, `\######(`, "pkl"},
		{`\=`, `\=`, "circom/brightscript"},
		{`\;`, `\;`, "tmux/cmake"},
		{`\"`, `\"`, "twig"},
		{`\]`, `\]`, "org/mermaid"},
		{`\)`, `\)`, "org"},
		{`\/`, `\/`, "tlaplus"},
		{`/\`, `/\`, "tlaplus"},
		{`\{`, `\{`, "java/luau"},
		{`\`, `\`, "gitignore/hack/many"},
		{`\\`, `\\`, "elixir"},
		// regex-source token names — C keeps pattern source verbatim
		{`\.not\.`, `\.not\.`, "fortran"},
		{`\.eqv\.`, `\.eqv\.`, "fortran"},
		// no backslash at all — fast path
		{`(`, `(`, "any"},
		{`module`, `module`, "any"},
	}
	for _, c := range cases {
		if got := unescapePunctuationSymbolName(c.in); got != c.want {
			t.Errorf("%s: unescapePunctuationSymbolName(%q) = %q, want %q", c.grammar, c.in, got, c.want)
		}
	}
}
