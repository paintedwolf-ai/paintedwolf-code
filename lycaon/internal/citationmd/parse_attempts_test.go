package citationmd

import (
	"reflect"
	"strings"
	"testing"
)

func registeredKinds(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, name := range names {
		set[name] = true
	}
	return func(kind string) bool { return set[kind] }
}

// What counts as a citation attempt, row by row. Withholding a correct message
// costs more than missing a doubtful citation, so syntax that could be prose needs
// a structural signal.
func TestCitationAttempts(t *testing.T) {
	t.Parallel()
	opts := Options{KnownHandleKind: registeredKinds("read", "grep", "task", "command"), KnownPath: registeredKinds("Dockerfile", "a/b.go", "docs/x.md", "@shared/a.go")}
	cases := []struct {
		name, source string
		occurrences  []string
		issues       []IssueCode
	}{
		{"M1 index expression", "array[i:2]", nil, nil},
		{"M1 index with name", "foo[bar:1]", nil, nil},
		{"index with a path shape", "foo[a.go:1]", nil, nil},
		{"index with a known name", "foo[Dockerfile]", nil, nil},
		{"index with a quoted coordinate", `foo["a b":1]`, nil, nil},
		{"CJK prose is no identifier", "設定は[a.go:3]を参照", []string{"a.go:3"}, nil},
		{"handle after a word", "done[read#1]", []string{"read#1"}, nil},
		{"directive after a word", "x[verify: targeted - y]", []string{"[verify: targeted - y]"}, nil},
		{"path with a dot", "[a.go:42]", []string{"a.go:42"}, nil},
		{"path with a slash", "[x/y:42]", []string{"x/y:42"}, nil},
		{"M1 unknown name with coordinate", "[note:3]", nil, nil},
		{"known name with coordinate", "[Dockerfile:3]", []string{"Dockerfile:3"}, nil},
		{"M1 digits and dots", "[1.5:2]", nil, nil},
		{"time", "[10:30]", nil, nil},
		{"M1 date", "[2024-01-15:3]", nil, nil},
		{"known bare", "[Dockerfile]", []string{"Dockerfile"}, nil},
		{"known multi-segment", "[a/b.go]", []string{"a/b.go"}, nil},
		{"known root label", "[@shared/a.go]", []string{"@shared/a.go"}, nil},
		{"low dot", "[.]", nil, nil},
		{"M1 quoted group", `["a", "b"]`, nil, nil},
		{"M1 quoted words", `["hello world"]`, nil, nil},
		{"M1 quoted flag", `["--flag"]`, nil, nil},
		{"quoted key", `["key": 3]`, nil, nil},
		{"known quoted", `["docs/x.md"]`, []string{`"docs/x.md"`}, nil},
		{"quoted coordinate", `["a b":12]`, []string{`"a b":12`}, nil},
		{"quoted bad coordinate", `["a b":12x]`, nil, []IssueCode{IssueMalformedCitation}},
		{"bare with a word excerpt", `[foo "bar"]`, nil, nil},
		{"registered kind", "[read#3]", []string{"read#3"}, nil},
		{"task through the predicate", "[task#1]", []string{"task#1"}, nil},
		{"M1 unregistered pr", "[pr#123]", nil, nil},
		{"M1 unregistered issue", "[issue#12]", nil, nil},
		{"M1 anchor", "[section#intro]", nil, nil},
		{"M1 language", "[c#]", nil, nil},
		{"M1 language capital", "[C#]", nil, nil},
		{"registered kind junk", "[read#x]", nil, []IssueCode{IssueMalformedCitation}},
		{"M1 reference", "[cite: Smith 2020]", nil, nil},
		{"cite with no arrow", "[cite: a b => c]", nil, nil},
		{"repair", "[cite: c1 => read#1]", []string{"[cite: c1 => read#1]"}, nil},
		{"H7 known then handle", "[a/b.go, read#9]", []string{"a/b.go", "read#9"}, nil},
		{"H7 unknown then handle", "[sic, read#9]", []string{"read#9"}, []IssueCode{IssueMalformedCitation}},
		{"H7 known then coordinate", "[Dockerfile, a.go:3]", []string{"Dockerfile", "a.go:3"}, nil},
		{"H7 unknown then coordinate", "[alpha, a.go:3]", []string{"a.go:3"}, []IssueCode{IssueMalformedCitation}},
		{"H7 coordinate then unknown", "[a.go:3, alpha]", []string{"a.go:3"}, []IssueCode{IssueMalformedCitation}},
		{"H7 unregistered then handle", "[pr#1, read#9]", []string{"read#9"}, []IssueCode{IssueMalformedCitation}},
		{"inert group", "[alpha, beta]", nil, nil},
		{"M4 compiler form", "[a.go:42:10]", nil, []IssueCode{IssueMalformedCitation}},
		{"M4 line fragment", "[a.go#L42]", nil, []IssueCode{IssueMalformedCitation}},
		{"M4 L coordinate", "[a.go:L42]", nil, []IssueCode{IssueMalformedCitation}},
		{"M4 open range", "[a.go:42-]", nil, []IssueCode{IssueMalformedCitation}},
		{"M4 spaced verify", "[verify : x]", nil, []IssueCode{IssueInvalidVerification}},
		{"spaced artifact", "[artifact : x]", nil, []IssueCode{IssueMalformedArtifact}},
		{"spaced cite", "[cite : c1 => read#1]", nil, []IssueCode{IssueMalformedRepair}},
		{"H6 handle in namespace verify", "[verify:read#1]", []string{"verify:read#1"}, nil},
		{"H6 handle in namespace cite", "[cite:read#1:2]", []string{"cite:read#1:2"}, nil},
		{"H6 handle in namespace artifact", "[artifact:grep#2 a.go:3]", []string{"artifact:grep#2 a.go:3"}, nil},
		{"verify of an unregistered kind", "[verify:pr#1]", nil, []IssueCode{IssueInvalidVerification}},
		{"verify and a number", "[verify:3]", nil, []IssueCode{IssueInvalidVerification}},
		{"H6 namespace out of the pattern", "[leg.1:read#1]", nil, []IssueCode{IssueMalformedCitation}},
		{"url with a port is no coordinate", "[www.x.test:8080]", []string{"www.x.test:8080"}, nil},
		{"link with a line", "[x](y.go#L3)", []string{"[x](y.go#L3)"}, nil},
		{"M1 ordinary link", "[x](y)", nil, nil},
		{"known link", "[x](a/b.go)", []string{"[x](a/b.go)"}, nil},
		{"H5 heading anchor on a known file", "[x](docs/x.md#section-2)", []string{"[x](docs/x.md#section-2)"}, nil},
		{"H5 heading anchor elsewhere", "[x](README.md#install)", nil, nil},
		{"H5 column fragment", "[x](a/b.go#L3C5)", []string{"[x](a/b.go#L3C5)"}, nil},
		{"H5 backward range", "[x](a/b.go#L9-L2)", nil, []IssueCode{IssueInvalidRange}},
		{"H5 query", "[x](a/b.go?x=1#L3)", []string{"[x](a/b.go?x=1#L3)"}, nil},
		{"low malformed source", "[x](source:///a.go#L1)", nil, []IssueCode{IssueMalformedCitation}},
		{"low source query", "[x](source://r/a.go?unknown=x#L1)", nil, []IssueCode{IssueMalformedCitation}},
		{"raw anchors claim nothing", `a <a href="https://x.test">b</a>`, nil, nil},
		{"N2 digits and dots stay prose", "[1.5:10:30]", nil, nil},
		{"N2 host and port is an attempt", "[example.com:8080]", []string{"example.com:8080"}, nil},
		{"N3 numeric fragment is no coordinate", "[v](v2#3)", nil, nil},
		{"N3 line fragment needs its L", "[a](a.go#42)", nil, nil},
		{"web scheme with no slashes", "[x](https:unobs.test)", []string{"[x](https:unobs.test)"}, nil},
		{"B3 escaped-looking URL", `\https://unobs.test/a`, []string{"https://unobs.test/a"}, nil},
		{"B3 escaped-looking www", `\www.unobs.test`, []string{"www.unobs.test"}, nil},
		{"N1 URL closing a worded bracket", "[see https://seen.test/a]", []string{"https://seen.test/a"}, nil},
		{"N1 URL closing a labelled bracket", "[docs: https://seen.test/a]", []string{"https://seen.test/a"}, nil},
		{"closed bracket earlier leaves it in the URL", "[a] https://seen.test/a]", []string{"https://seen.test/a]"}, nil},
		{"URL before a released bracket", "[https://a.test/b [c] x]", []string{"https://a.test/b"}, nil},
		{"URL in a released bracket and after it", "[x, https://a.test/b] https://c.test/d", []string{"https://a.test/b", "https://c.test/d"}, nil},
		{"rescan sees a stop the first look passed", "[a/b, https://a.test/ c] https://c.test/d", []string{"https://a.test/", "https://c.test/d"}, nil},
		{"N1 ideographic space ends a URL", "https://seen.test/a　次", []string{"https://seen.test/a"}, nil},
		{"N1 no-break space ends a URL", "https://seen.test/a\u00a0x", []string{"https://seen.test/a"}, nil},
		{"N1 em space ends a URL", "https://seen.test/a\u2003x", []string{"https://seen.test/a"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, opts)
			var tokens []string
			for _, o := range doc.Occurrences {
				if tc.source[o.Span.Start:o.Span.End] != o.Token {
					t.Fatalf("span %+v does not hold %q", o.Span, o.Token)
				}
				tokens = append(tokens, o.Token)
			}
			if !reflect.DeepEqual(tokens, tc.occurrences) {
				t.Fatalf("occurrences = %q, want %q", tokens, tc.occurrences)
			}
			if got := issueCodes(doc); !reflect.DeepEqual(got, tc.issues) {
				t.Fatalf("issues = %+v", doc.Issues)
			}
		})
	}
}

// With no registry, every kind the grammar allows counts.
func TestNilKindRegistryAcceptsTheGrammar(t *testing.T) {
	t.Parallel()
	if doc := Parse("[pr#123]", Options{}); len(doc.Occurrences) != 1 {
		t.Fatalf("occurrences = %+v", doc.Occurrences)
	}
	if doc := Parse("[Pr#123]", Options{}); len(doc.Occurrences) != 0 {
		t.Fatalf("occurrences = %+v", doc.Occurrences)
	}
}

// Low: a member's token and span stop at the member.
func TestMemberSpansExcludeWhitespace(t *testing.T) {
	t.Parallel()
	doc := Parse("[read#1 , read#2 ]", Options{})
	if len(doc.Occurrences) != 2 || doc.Occurrences[0].Token != "read#1" || doc.Occurrences[1].Token != "read#2" {
		t.Fatalf("occurrences = %+v", doc.Occurrences)
	}
}

// Low: a code span in a reason is opaque, and bracket escapes do not stay in it.
func TestVerifyReasonSyntax(t *testing.T) {
	t.Parallel()
	cases := []struct{ source, reason string }{
		{"[verify: targeted - ran `a]b` ok]", "ran `a]b` ok"},
		{"[verify: targeted - ran ``a`]b`` ok]", "ran ``a`]b`` ok"},
		{"[verify: targeted - lone ` tick]", "lone ` tick"},
		{`[verify: targeted - a \] b \[ c]`, "a ] b [ c"},
	}
	for _, tc := range cases {
		doc := Parse(tc.source, Options{})
		if len(doc.Issues) != 0 || len(doc.Occurrences) != 1 || doc.Occurrences[0].Verify.Reason != tc.reason {
			t.Errorf("%s: %+v / %+v", tc.source, doc.Occurrences, doc.Issues)
		}
	}
}

// Low: issues are budgeted like occurrences.
func TestIssueBudget(t *testing.T) {
	t.Parallel()
	doc := Parse(strings.Repeat("[read#x] ", 5), Options{MaxOccurrences: 2})
	if len(doc.Issues) != 3 || doc.Issues[2].Code != IssueOccurrenceBudget || doc.Issues[2].Token != "3" {
		t.Fatalf("issues = %+v", doc.Issues)
	}
}

// M8: removing a node never creates Markdown, and every removal is checked by
// parsing the result again.
func TestRenderCreatesNoStructure(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, source, want string }{
		{"emphasis", "**[read#9]**", "** **"},
		{"glued word", "foo[task#1]bar", "foo bar"},
		{"split URL", "h[read#9]ttps://unobs.test/j", "h ttps://unobs.test/j"},
		{"split www", "www[read#9].unobs.test/k", "www .unobs.test/k"},
		{"new autolink", "<[read#9]https://x.test>", "< https://x.test>"},
		{"standard link stays", "[[read#9]](x)", "[[read#9]](x)"},
		{"B1 URL in a directive", "_[verify: targeted - see https://unobs.test/v]_", "_ _"},
		{"B1 URL in an excerpt", `**[task#1 "https://unobs.test/e"]**`, "** **"},
		{"B1 directive body", "x[verify: targeted - checked it]y", "x y"},
		{"B1 damaged directive", "**[read#9 \"[x](https://unobs.test)\"]**", "** **"},
		{"N4 new definition", "[a][read#9]: other.md", "[a] : other.md"},
		{"N4 line-start definition", "[read#9][a]: other.md", "\\[a]: other.md"},
		{"N4 thematic break", "[read#9]---", "\\---"},
		{"N4 setext underline", "para\n[read#9]---\nb", "para\n\\---\nb"},
		{"N4 stars", "[read#9]***", "\\***"},
		{"N4 paragraphs do not merge", "a\n[read#9]\n\nb", "a\n\nb"},
		{"N4 item does not take the next paragraph", "- a\n- [read#9]\n\nb", "- a\n\nb"},
		{"N4 quote does not take the next paragraph", "> a\n> [read#9]\n\nb", "> a\n\nb"},
		{"N4 continuation stays in its item", "- a\n\n  [read#9]\n      foo", "- a\n\n  foo"},
		{"N4 double bracket", "[[read#9]]", "[ ]"},
		{"list item", "- [read#9]", ""},
		{"list item among others", "- a\n- [read#9]\n- b", "- a\n\n- b"},
		{"ordered item", "1. [read#9]", ""},
		{"heading", "# [read#9]", ""},
		{"quote", "> [read#9]", ""},
		{"quote among others", "> a\n> [read#9]\n> b", "> a\n\n> b"},
		{"nested", "> - # [read#9] [task#1]", ""},
		{"markup only", "---[read#9]", ""},
		{"indented continuation", "para\n\n[read#9]\n    foo", "para\n\nfoo"},
		{"paragraph line after", "a\n[read#9]\nb", "a\n\nb"},
		{"marker after", "[read#9] - x", "\\- x"},
		{"heading after", "[read#9] # x", "\\# x"},
		{"fence after", "[read#9] ``` x", "\\``` x"},
		{"ordered marker after", "[read#9] 1. x", "&#49;. x"},
		{"html after", "[read#9]<div>", "\\<div>"},
		{"words stay apart", "a [read#9]b", "a b"},
		{"before punctuation", "a [read#9].", "a."},
		{"between words", "a [read#9] b", "a b"},
		{"glued before punctuation", "a[read#9].", "a."},
		{"glued before more text", "a[read#9].b", "a .b"},
		{"literal on the line stays", "- `x` [read#9]", "- `x`"},
		{"damaged node", "- [read#x]", ""},
		{"crlf line", "[read#9]\r\n\r\nNext\r\n", "Next"},
		{"crlf between", "a\r\n[read#9]\r\nb", "a\r\n\nb"},
		{"crlf fence document", "```md\r\n[read#9]\r\n```\r\n", ""},
		{"crlf unterminated", "[verify: x\r\nnext", "next"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			got := Render(doc, func(o Occurrence) Rendition { return Rendition{Keep: o.Link != nil} })
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			before, after := Parse(tc.source, Options{}), Parse(got, Options{})
			if zones := countLinks(after); zones > countLinks(before) {
				t.Fatalf("%q created a link", got)
			}
		})
	}
}

func countLinks(doc Document) int {
	count := 0
	for _, zone := range doc.Zones {
		if zone.Kind == ZoneLink || zone.Kind == ZoneAutolink || zone.Kind == ZoneFence || zone.Kind == ZoneIndented {
			count++
		}
	}
	return count
}

// A kept bare link in brackets renders without the bracket in its destination.
func TestBracketedBareLinkPresentation(t *testing.T) {
	t.Parallel()
	if got := Project(Parse("[https://x.test/a]", Options{})); got != "[<https://x.test/a>]" {
		t.Fatalf("got %q", got)
	}
	if got := Project(Parse("[see https://x.test/a]", Options{})); got != "[see <https://x.test/a>]" {
		t.Fatalf("got %q", got)
	}
	if got := PlainLabel(Link{Label: "see [read#3] now"}); got != "see now" {
		t.Fatalf("label = %q", got)
	}
	if got := PlainLabel(Link{Label: "a https://x.test/p and WWW.y.test"}); got != "a https&#58;//x.test/p and WWW&#46;y.test" {
		t.Fatalf("label = %q", got)
	}
	bare := Link{Form: LinkAutolink, Label: "https://x.test/u[t](https://x.test/t)"}
	if got := PlainLabel(bare); got != `https&#58;//x.test/u\[t\]\(https&#58;//x.test/t\)` {
		t.Fatalf("bare label = %q", got)
	}
	if got := Project(Parse("[<https://x.test/c> y](z)", Options{})); got != "[&lt;https://x.test/c> y](z)" {
		t.Fatalf("autolink in a label = %q", got)
	}
}

// Whole-response fence and recovery under CRLF.
func TestCRLFSpans(t *testing.T) {
	t.Parallel()
	source := "```markdown\r\nAt [read#3:4].\r\n\r\nNext.\r\n```\r\n"
	doc := Parse(source, Options{})
	if len(doc.Occurrences) != 1 || doc.Occurrences[0].Token != "read#3:4" {
		t.Fatalf("occurrences = %+v, zones = %+v", doc.Occurrences, doc.Zones)
	}
	if got := Project(doc); got != "At.\r\n\r\nNext." {
		t.Fatalf("got %q", got)
	}
	bad := Parse("[read#3 \"x\r\nnext", Options{})
	if len(bad.Issues) != 1 || bad.Issues[0].Span != (Span{0, 10}) {
		t.Fatalf("issues = %+v", bad.Issues)
	}
}

// Back-pedalling matches the frontend's rule on the edges fixtures cannot reach.
func TestBackpedalURL(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"https://x.test/a", "https://x.test/a"},
		{"https://x.test/a.", "https://x.test/a"},
		{"https://x.test/a...", "https://x.test/a"},
		{"https://x.test/a(b)", "https://x.test/a(b)"},
		{"https://x.test/a(b).", "https://x.test/a(b)"},
		{"https://x.test/a)", "https://x.test/a"},
		{"https://x.test/a(b", "https://x.test/a"},
		{"https://x.test/a&amp;", "https://x.test/a"},
		{"https://x.test/a&;", "https://x.test/a&"},
		{"https://x.test/a;", "https://x.test/a"},
		{"https://x.test/a b;", "https://x.test/a b"},
		{"...", ""},
	}
	for _, tc := range cases {
		if got := backpedalURL(tc.in); got != tc.want {
			t.Errorf("backpedalURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// B3: raw anchors are escaped outright, in every form, and their text stays. Code is
// not touched.
func TestRawAnchorsAreEscaped(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, source, want string }{
		{"plain", `a <a href="https://x.test">b</a> c`, `a &lt;a href="https&#58;//x&#46;test"&gt;b&lt;/a&gt; c`},
		{"quoted bracket", `<a title=">" href="https://x.test">b</a>`, `&lt;a title="&gt;" href="https&#58;//x&#46;test"&gt;b&lt;/a&gt;`},
		{"entity scheme", `x <a href="https&#58;//x.test">b</a>`, `x &lt;a href="https&#58;//x&#46;test"&gt;b&lt;/a&gt;`},
		{"slash attribute in a block", `<div><a/href="https://x.test">b</a></div>`, `<div>&lt;a/href="https&#58;//x&#46;test"&gt;b&lt;/a&gt;</div>`},
		{"upper case", `x <A HREF="https://x.test">b</A>`, `x &lt;A HREF="https&#58;//x&#46;test"&gt;b&lt;/A&gt;`},
		{"area", `<area href="https://x.test">`, `&lt;area href="https&#58;//x&#46;test"&gt;`},
		{"multi-line", "x <a\nhref=\"https://x.test\">b</a>", "x &lt;a\nhref=\"https&#58;//x&#46;test\"&gt;b&lt;/a&gt;"},
		{"other tags stay", `x <abbr title="y">z</abbr> <a-b>`, `x <abbr title="y">z</abbr> <a-b>`},
		{"prose comparison", `a <b and c>d`, `a <b and c>d`},
		{"code span stays", "`<a href=\"https://x.test\">`", "`<a href=\"https://x.test\">`"},
		{"fence stays", "```\n<a href=\"https://x.test\">\n```", "```\n<a href=\"https://x.test\">\n```"},
		{"host link stays", "[a/b c.go:1]", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			got := Project(doc)
			want := tc.want
			if want == "" {
				want = tc.source
			}
			if tc.name != "host link stays" && got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
	if got := Project(Parse(`["a/b c.go":1]`, Options{})); got != "[a/b c.go:1](<a/b c.go#L1>)" {
		t.Fatalf("host-written link = %q", got)
	}
	if got := Project(Parse("[<a href=\"https://x.test\">y</a>](<a/c d.go#L1>)", Options{})); !strings.Contains(got, "&lt;a href") || !strings.Contains(got, "(<a/c d.go#L1>)") {
		t.Fatalf("label and destination = %q", got)
	}
}

// B3: a URL past the node cap is one rejected unit; no prefix stands in for it.
func TestOverlongBareURL(t *testing.T) {
	t.Parallel()
	long := "https://pre.test/" + strings.Repeat("b", MaxInlineProtocolNodeBytes-len("https://pre.test/"))
	exact := Parse(long, Options{})
	if len(exact.Occurrences) != 1 || exact.Occurrences[0].Link.Destination != long {
		t.Fatalf("at the cap: %+v", exact.Occurrences)
	}
	source := "see " + long + "EVIL tail"
	doc := Parse(source, Options{})
	if len(doc.Occurrences) != 0 || len(doc.Issues) != 1 || doc.Issues[0].Code != IssueNodeTooLong || doc.Issues[0].Kind != KindWebLink {
		t.Fatalf("occurrences = %d, issues = %+v", len(doc.Occurrences), doc.Issues)
	}
	if got := Project(doc); got != "see tail" {
		t.Fatalf("presented %q", got)
	}
}

// A malformed link keeps only its label; no link survives it.
func TestBrokenLinkPresentsItsLabel(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"[a](source:///a.go#L1)", "[a](source:x)", "[a](source://r/a.go?unknown=x)", "[a](y.go#L0)", "[a](y.go#L5-L2)"} {
		doc := Parse(source, Options{})
		if len(doc.Issues) != 1 {
			t.Errorf("%s: issues = %+v", source, doc.Issues)
		}
		if got := Project(doc); got != "a" {
			t.Errorf("%s: presented %q", source, got)
		}
	}
}
