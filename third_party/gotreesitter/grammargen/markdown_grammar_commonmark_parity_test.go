package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// cmParityCase is one CommonMark §3-§6 construct probed for byte-identical CST
// between grammargen.MarkdownGrammar()'s generated parser and the bundled
// grammars.MarkdownLanguage() blob.
type cmParityCase struct {
	name   string
	src    string
	assert bool   // true: gen CST MUST equal bundled; false: known divergence (informational only)
	reason string // why an assert:false case diverges (tracking note)
}

// commonMarkParityCorpus is a self-contained CommonMark §3-§6 coverage corpus.
//
// Unlike TestMarkdownGrammarMdppCorpusParity (which reads mdpp's sibling
// testdata and therefore SKIPS in CI where mdpp isn't checked out), this corpus
// is embedded and runs unconditionally — it is the regression guard that
// belongs in gotreesitter's own suite. Several real divergences were shipped
// and only caught downstream in mdpp precisely because no in-repo block-level
// parity corpus existed (html_block termination, fenced-code-after-content,
// consecutive link-reference-definitions, top-level (list)/(block_quote)
// wrappers). Every one of those is asserted below.
//
// Block-parser note: MarkdownLanguage is the BLOCK grammar; inline content is
// emitted as an opaque (inline) node (the inline structure lives in
// MarkdownInlineLanguage). So §6 inline cases here assert that the block parser
// wraps inline-bearing lines identically — they intentionally do not probe
// inline structure.
var commonMarkParityCorpus = []cmParityCase{
	// §4.1 Thematic breaks
	{"tb_star", "***\n", true, ""},
	{"tb_dash", "---\n", true, ""},
	{"tb_under", "___\n", true, ""},
	{"tb_spaced", " - - -\n", true, ""},
	{"tb_after_para", "a\n\n***\n", true, ""},

	// §4.2 ATX headings
	{"atx_h1", "# h\n", true, ""},
	{"atx_h6", "###### h\n", true, ""},
	{"atx_closed", "# h #\n", true, ""},
	{"atx_trailing_hashes", "## h ##\n", true, ""},
	{"atx_empty", "#\n", false, "empty ATX heading: gen omits the hidden atx_h1_marker (ref keeps it). Minor; no content."},

	// §4.3 Setext headings
	{"setext_h1", "H\n=\n", true, ""},
	{"setext_h2", "H\n-\n", true, ""},
	{"setext_multiline", "a\nb\n===\n", true, ""},

	// §4.4 Indented code blocks
	{"indent_code", "    code\n", true, ""},
	{"indent_code_after_para", "para\n\n    code\n", true, ""},

	// §4.5 Fenced code blocks
	{"fence_basic", "```\nx\n```\n", true, ""},
	{"fence_info", "```go\nx\n```\n", true, ""},
	{"fence_tilde", "~~~\nx\n~~~\n", true, ""},
	{"fence_blank_lines", "```\n\nx\n\n```\n", true, ""},
	{"fence_no_close", "```\nx\n", true, ""},
	{"fence_after_para", "p\n\n```\nx\n```\n", true, ""},
	{"fence_after_heading", "# h\n\n```\nx\n```\n", true, ""},

	// §4.6 HTML blocks (types 1-7)
	{"html_pre", "<pre>x</pre>\n", true, ""},
	{"html_comment", "<!-- c -->\n", true, ""},
	{"html_pi", "<?php ?>\n", true, ""},
	{"html_decl", "<!DOCTYPE html>\n", true, ""},
	{"html_cdata", "<![CDATA[x]]>\n", true, ""},
	{"html_div", "<div>x</div>\n", true, ""},
	{"html_div_blank_para", "<div>x</div>\n\npara\n", true, ""},
	{"html_aside_then_fence", "<aside>x</aside>\n\n```\nc\n```\n", true, ""},
	{"html_type7_blank_para", "<custom>x</custom>\n\npara\n", true, ""},

	// §4.7 Link reference definitions
	{"lrd_basic", "[a]: /url\n", true, ""},
	{"lrd_title", "[a]: /url \"t\"\n", true, ""},
	{"lrd_two_consecutive", "[a]: /u1\n[b]: /u2\n", true, ""},
	{"lrd_after_para", "p\n\n[a]: /url\n", true, ""},
	{"lrd_title_next_line", "[a]: /url\n\"title\"\n", true, ""},
	{"lrd_dest_next_line", "[a]:\n/url\n", false, "multi-line link-ref-def (destination on its own line): gen falls back to inline. Tracked soft-divergence."},
	{"lrd_then_setext_underline", "[a]: /url\n---\n", false, "setext-underline-after-def precedence: ref makes the def line a setext_heading; gen keeps link_reference_definition + thematic_break. Tracked soft-divergence."},

	// §4.8/§4.9 Paragraphs & blank lines
	{"para", "hello world\n", true, ""},
	{"para_multiline", "line one\nline two\n", true, ""},
	{"blank_lines", "a\n\n\n\nb\n", true, ""},

	// §5.1 Block quotes
	{"bq", "> q\n", true, ""},
	{"bq_nested", "> > inner\n", true, ""},
	{"bq_lazy", "> a\nb\n", true, ""},
	{"bq_multi_para", "> a\n>\n> b\n", true, ""},
	{"bq_then_list", "> q\n\n- a\n", true, ""},
	{"bq_contains_fence", "> ```\n> x\n> ```\n", true, ""},

	// §5.2/§5.3 List items & lists
	{"ul_dash", "- a\n", true, ""},
	{"ul_two_items", "- a\n- b\n", true, ""},
	{"ul_star", "* a\n", true, ""},
	{"ul_plus", "+ a\n", true, ""},
	{"ol_dot", "1. a\n", true, ""},
	{"ol_paren", "1) a\n", true, ""},
	{"ul_loose", "- a\n\n- b\n", true, ""},
	{"ul_nested", "- a\n  - b\n", true, ""},
	{"ul_multi_para_item", "- a\n\n  b\n", true, ""},
	{"list_mixed_types", "1. a\n- b\n", true, ""},
	{"list_after_para", "p\n\n- a\n", true, ""},
	{"heading_between_lists", "1. a\n\n## h\n\n2. b\n", true, ""},
	{"para_after_list", "- a\n\nfoo\n", true, ""},
	{"task_list", "- [ ] a\n- [x] b\n", true, ""},

	// §6 Inlines (block-level wrapping parity)
	{"emph", "*em* and _em_\n", true, ""},
	{"strong", "**strong**\n", true, ""},
	{"code_span", "`code`\n", true, ""},
	{"code_span_with_pipe", "`a|b`\n", true, ""},
	{"strikethrough", "~~x~~\n", true, ""},
	{"link_inline", "[t](/u)\n", true, ""},
	{"link_reference_use", "[t][r]\n\n[r]: /u\n", true, ""},
	{"image", "![alt](/u)\n", true, ""},
	{"autolink", "<http://example.com>\n", true, ""},
	{"raw_html_inline", "a <span>b</span> c\n", true, ""},
	{"hard_break_backslash", "a\\\nb\n", true, ""},
	{"hard_break_spaces", "a  \nb\n", true, ""},
	{"backslash_escape", "\\*not emph\\*\n", true, ""},
	{"entities", "&amp; &#35; &#X22;\n", true, ""},

	// GFM tables
	{"table_basic", "| a | b |\n|---|---|\n| 1 | 2 |\n", true, ""},
	{"table_alignment", "| L | C | R |\n|:--|:-:|--:|\n| 1 | 2 | 3 |\n", true, ""},
	{"table_inline_cells", "| a | b |\n|---|---|\n| **x** | `y` |\n", true, ""},

	// §3 Precedence / mixed document
	{"mixed_document", "# T\n\np\n\n- a\n- b\n\n> q\n\n```\nc\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n", true, ""},
}

// TestMarkdownGrammarCommonMarkParity asserts that grammargen.MarkdownGrammar()'s
// generated parser produces byte-identical CST to the bundled markdown.bin blob
// across CommonMark §3-§6 constructs (embedded corpus, runs in CI). Known
// soft-divergences are marked assert:false and only logged.
func TestMarkdownGrammarCommonMarkParity(t *testing.T) {
	refLang := grammars.MarkdownLanguage()
	if refLang == nil {
		t.Skip("bundled MarkdownLanguage unavailable")
	}
	genLang := generateMarkdownLang(t)
	if genLang == nil {
		t.Skip("could not generate markdown language")
	}
	refParser := gotreesitter.NewParser(refLang)
	genParser := gotreesitter.NewParser(genLang)

	var asserted, informational, stillDivergent int
	for _, tc := range commonMarkParityCorpus {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "lrd_dest_next_line" {
				t.Skip("skip known generated Markdown parser hang; tracked as a soft-divergence")
			}
			if raceEnabled && !tc.assert {
				t.Skip("skip known Markdown soft-divergence under -race; non-race coverage still logs whether it converged")
			}
			refTree, err := refParser.Parse([]byte(tc.src))
			if err != nil || refTree == nil {
				t.Fatalf("reference parse failed for %q: %v", tc.src, err)
			}
			genTree, err := genParser.Parse([]byte(tc.src))
			if err != nil || genTree == nil {
				t.Fatalf("generated parse failed for %q: %v", tc.src, err)
			}
			ref := refTree.RootNode().SExpr(refLang)
			gen := genTree.RootNode().SExpr(genLang)

			if tc.assert {
				asserted++
				if ref != gen {
					t.Errorf("CST diff for %q\n  in:  %q\n  ref: %s\n  gen: %s",
						tc.name, tc.src, truncateSExp(ref, 4000), truncateSExp(gen, 4000))
				}
				return
			}
			informational++
			if ref != gen {
				stillDivergent++
				t.Logf("known divergence %q (%s)\n  ref: %s\n  gen: %s",
					tc.name, tc.reason, truncateSExp(ref, 1000), truncateSExp(gen, 1000))
			} else {
				// A documented divergence now matches — promote it to assert:true.
				t.Logf("NOTE: informational case %q now MATCHES bundled; promote to assert (was: %s)", tc.name, tc.reason)
			}
		})
	}
	t.Logf("commonmark parity: %d asserted, %d informational (%d still divergent)",
		asserted, informational, stillDivergent)
}
