package citationmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/verification"
)

func knownNames(names ...string) Options {
	set := map[string]bool{}
	for _, name := range names {
		set[name] = true
	}
	return Options{KnownPath: func(name string) bool { return set[name] }}
}

func citations(doc Document) []Citation {
	var out []Citation
	for _, o := range doc.Occurrences {
		if o.Citation != nil {
			out = append(out, *o.Citation)
		}
	}
	return out
}

func issueCodes(doc Document) []IssueCode {
	var out []IssueCode
	for _, issue := range doc.Issues {
		out = append(out, issue.Code)
	}
	return out
}

func TestParseCitationForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		source string
		opts   Options
		want   []Citation
	}{
		{"bare handle", "See [read#3].", Options{}, []Citation{{Handle: "read#3"}}},
		{"AC-AST-03 namespaced handle", "[leg-a:read#3]", Options{}, []Citation{{Handle: "leg-a:read#3"}}},
		{"nested namespace", "[a:b_c:grep#12]", Options{}, []Citation{{Handle: "a:b_c:grep#12"}}},
		{"dispatch handle", "[task#1]", Options{}, []Citation{{Handle: "task#1"}}},
		{"handle line", "[read#3:42]", Options{}, []Citation{{Handle: "read#3", Line: 42}}},
		{"handle range", "[read#3:42-58]", Options{}, []Citation{{Handle: "read#3", Line: 42, EndLine: 58}}},
		{"one line range normalizes", "[read#3:42-42]", Options{}, []Citation{{Handle: "read#3", Line: 42}}},
		{"handle excerpt", `[read#3:42 "if err != nil"]`, Options{}, []Citation{{Handle: "read#3", Line: 42, Excerpt: "if err != nil"}}},
		{"bare handle excerpt", `[read#3 "if err != nil"]`, Options{}, []Citation{{Handle: "read#3", Excerpt: "if err != nil"}}},
		{"handle plus path", "[leg-a:grep#2 internal/a.go:42]", Options{}, []Citation{{Handle: "leg-a:grep#2", Path: "internal/a.go", Line: 42}}},
		{"handle plus single segment path", "[grep#2 Dockerfile:3]", Options{}, []Citation{{Handle: "grep#2", Path: "Dockerfile", Line: 3}}},
		{"handle plus quoted path", `[grep#2 "docs/a b.md":3]`, Options{}, []Citation{{Handle: "grep#2", Path: "docs/a b.md", Line: 3}}},
		{"path coordinate", "[internal/a.go:42]", Options{}, []Citation{{Path: "internal/a.go", Line: 42}}},
		{"path range", "[internal/a.go:42-58]", Options{}, []Citation{{Path: "internal/a.go", Line: 42, EndLine: 58}}},
		{"AC-AST-03 root label", "[@shared/lib/sec.go:12]", Options{}, []Citation{{Path: "@shared/lib/sec.go", Line: 12}}},
		{"observed bare path", "[internal/a.go]", knownNames("internal/a.go"), []Citation{{Path: "internal/a.go"}}},
		{"AC-AST-16 single segment attempt", "[a.go:42]", Options{}, []Citation{{Path: "a.go", Line: 42}}},
		{"AC-AST-16 multi-segment attempt", "[x/y.go:42]", Options{}, []Citation{{Path: "x/y.go", Line: 42}}},
		{"attempt with a range", "[LICENSE:3-4]", Options{}, []Citation{{Path: "LICENSE", Line: 3, EndLine: 4}}},
		{"attempt with an excerpt", `[a.go:3 "some text"]`, Options{}, []Citation{{Path: "a.go", Line: 3, Excerpt: "some text"}}},
		{"AC-AST-03 known extensionless", "[Dockerfile]", knownNames("Dockerfile"), []Citation{{Path: "Dockerfile"}}},
		{"AC-AST-05 observed LICENSE", "[LICENSE]", knownNames("LICENSE"), []Citation{{Path: "LICENSE"}}},
		{"known with coordinate", "[LICENSE:3]", knownNames("LICENSE"), []Citation{{Path: "LICENSE", Line: 3}}},
		{"known with excerpt", `[LICENSE "MIT License"]`, knownNames("LICENSE"), []Citation{{Path: "LICENSE", Excerpt: "MIT License"}}},
		{"AC-AST-03 quoted space", `["docs/design notes.md":12]`, Options{}, []Citation{{Path: "docs/design notes.md", Line: 12}}},
		{"quoted path without coordinate", `["docs/design notes.md"]`, Options{}, []Citation{{Path: "docs/design notes.md"}}},
		{"quoted path excerpt", `["a b.md":1 "some text"]`, Options{}, []Citation{{Path: "a b.md", Line: 1, Excerpt: "some text"}}},
		{"path excerpt", `[a/b.go:1 "some text"]`, Options{}, []Citation{{Path: "a/b.go", Line: 1, Excerpt: "some text"}}},
		{"AC-AST-03 unicode path", "[docs/設計.md:7]", Options{}, []Citation{{Path: "docs/設計.md", Line: 7}}},
		{"AC-AST-03 quoted unicode", `["docs/設計 ノート.md":7]`, Options{}, []Citation{{Path: "docs/設計 ノート.md", Line: 7}}},
		{"group", "[read#3:42, leg-a:grep#2:18]", Options{}, []Citation{{Handle: "read#3", Line: 42}, {Handle: "leg-a:grep#2", Line: 18}}},
		{"group of paths", "[a/b.go:1, c/d.go:2]", Options{}, []Citation{{Path: "a/b.go", Line: 1}, {Path: "c/d.go", Line: 2}}},
		{"group after path", "[a/b.go, read#1]", knownNames("a/b.go"), []Citation{{Path: "a/b.go"}, {Handle: "read#1"}}},
		{"group tolerates space before comma", "[a/b.go , c/d.go]", knownNames("a/b.go", "c/d.go"), []Citation{{Path: "a/b.go"}, {Path: "c/d.go"}}},
		{"AC-AST-08 comma and semicolon", `[read#1:2 "a, b; c", read#2]`, Options{}, []Citation{{Handle: "read#1", Line: 2, Excerpt: "a, b; c"}, {Handle: "read#2"}}},
		{"AC-AST-08 escaped quote", `[read#1:2 "say \"hi\" now"]`, Options{}, []Citation{{Handle: "read#1", Line: 2, Excerpt: `say "hi" now`}}},
		{"AC-AST-08 brackets", `[read#1:2 "arr[0] = x]"]`, Options{}, []Citation{{Handle: "read#1", Line: 2, Excerpt: "arr[0] = x]"}}},
		{"AC-AST-08 escaped brackets", `[read#1:2 "arr\[0\] = \\x"]`, Options{}, []Citation{{Handle: "read#1", Line: 2, Excerpt: `arr[0] = \x`}}},
		{"excerpt holding code", "[read#1:2 \"a `b` c\"]", Options{}, []Citation{{Handle: "read#1", Line: 2, Excerpt: "a `b` c"}}},
		{"excerpt holding a link", `[read#1:2 "see [a](b) here"]`, Options{}, []Citation{{Handle: "read#1", Line: 2, Excerpt: "see [a](b) here"}}},
		{"workspace link", "[auth.go](internal/auth.go#L42)", Options{}, []Citation{{Path: "internal/auth.go", Line: 42}}},
		{"workspace link range", "[auth.go](internal/auth.go#L42-L58)", Options{}, []Citation{{Path: "internal/auth.go", Line: 42, EndLine: 58}}},
		{"workspace link spaces", "[n](<docs/a b.md#L3>)", Options{}, []Citation{{Path: "docs/a b.md", Line: 3}}},
		{"workspace link percent", "[n](docs/a%20b.md)", Options{}, []Citation{{Path: "docs/a b.md"}}},
		{"workspace link backward range keeps no coordinate", "[n](a/b.go#L9-L2)", Options{}, []Citation{{Path: "a/b.go#L9-L2"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, tc.opts)
			if len(doc.Issues) > 0 {
				t.Fatalf("issues = %+v", doc.Issues)
			}
			if got := citations(doc); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("citations = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseOccurrenceIdentity(t *testing.T) {
	t.Parallel()
	source := "x [read#1:2, read#1:9] y [read#1:2]"
	doc := Parse(source, Options{})
	if len(doc.Occurrences) != 3 {
		t.Fatalf("occurrences = %+v", doc.Occurrences)
	}
	for i, o := range doc.Occurrences {
		if o.Index != i || o.Kind != KindCitation {
			t.Fatalf("occurrence %d = %+v", i, o)
		}
		if source[o.Span.Start:o.Span.End] != o.Token {
			t.Fatalf("occurrence %d span %v does not hold token %q", i, o.Span, o.Token)
		}
	}
	if doc.Occurrences[0].Token != "read#1:2" || doc.Occurrences[1].Token != "read#1:9" {
		t.Fatalf("member tokens = %q, %q", doc.Occurrences[0].Token, doc.Occurrences[1].Token)
	}
	if doc.Occurrences[0].Node != doc.Occurrences[1].Node || source[doc.Occurrences[0].Node.Start:doc.Occurrences[0].Node.End] != "[read#1:2, read#1:9]" {
		t.Fatalf("group node = %v", doc.Occurrences[0].Node)
	}
}

func TestParseLeavesProseAlone(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, source string }{
		{"AC-AST-05 inline code path", "See `internal/a.go:42` here."},
		{"AC-AST-05 inline code handle", "`[read#1]`"},
		{"AC-AST-05 sic", "He said [sic] once."},
		{"AC-AST-05 draft", "A [draft] note."},
		{"AC-AST-16 and or", "[and/or]"},
		{"AC-AST-16 ratio", "[1/2]"},
		{"AC-AST-16 not applicable", "[n/a]"},
		{"unobserved bare path", "[internal/a.go]"},
		{"unobserved root path", "[@shared/a.go]"},
		{"digits with a coordinate", "[3:16]"},
		{"digit range", "[12:30-45]"},
		{"clock time", "[12:30-13:00]"},
		{"coordinate then prose", "[a.go:42 is wrong]"},
		{"unclosed attempt", "[a.go:42\nnext"},
		{"task box", "- [x] done\n- [X] done\n- [ ] open"},
		{"numeric index", "See [1] and [23]."},
		{"footnote", "See [^1]."},
		{"prose with spaces", "[see the docs for this]"},
		{"prose with slash and words", "[and/or maybe]"},
		{"colon prose", "[Note: careful]"},
		{"uppercase kind", "[Read#3]"},
		{"bare hash", "[#12]"},
		{"AC-AST-16 leg prefix prose", "[leg-of-lamb is tasty]"},
		{"AC-AST-16 unclosed prose", "An [open bracket\nthen more."},
		{"empty", "[]"},
		{"reserved word alone", "[verify]"},
		{"case-different reserved", "[Verify: targeted - x]"},
		{"group with unknown names", "[alpha, beta]"},
		{"AC-AST-04 escaped bracket", `\[read#1]`},
		{"AC-AST-04 escaped close", `\[read#1\]`},
		{"replacement character in path", "[a/\xff.go]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			if len(doc.Occurrences) != 0 || len(doc.Issues) != 0 {
				t.Fatalf("occurrences = %+v, issues = %+v", doc.Occurrences, doc.Issues)
			}
		})
	}
}

// AC-AST-04, AC-AST-14: literal syntax is never extracted and presents unchanged.
func TestParseLiteralZonesStayLiteral(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, source string
		kind         ZoneKind
	}{
		{"AC-AST-14 fence", "```\n[read#1] [verify: targeted - x]\n```", ZoneFence},
		{"tilde fence", "~~~txt\n[read#1]\n~~~", ZoneFence},
		{"long fence", "`````\n```\n[read#1]\n```\n`````", ZoneFence},
		{"AC-AST-04 fence in list", "- item\n\n  ```\n  [read#1]\n  ```", ZoneFence},
		{"AC-AST-04 fence in quote", "> ```\n> [read#1]\n> ```", ZoneFence},
		{"AC-AST-04 fence in quote in list", "- > ```\n  > [read#1]\n  > ```", ZoneFence},
		{"unclosed fence", "```\n[read#1]\n", ZoneFence},
		{"AC-AST-04 indented code", "para\n\n    [read#1]", ZoneIndented},
		{"indented code in list", "- a\n\n      [read#1]", ZoneIndented},
		{"AC-AST-04 html comment block", "<!-- [read#1] -->", ZoneHTMLBlock},
		{"multi-line comment block", "<!--\n[read#1]\n-->", ZoneHTMLBlock},
		{"html block", "<div>\n[read#1]\n</div>", ZoneHTMLBlock},
		{"AC-AST-04 inline comment", "a <!-- [read#1] --> b", ZoneRawHTML},
		{"inline tag attribute", `a <span title="[read#1]">b</span>`, ZoneRawHTML},
		{"AC-AST-04 inline code", "a `[read#1]` b", ZoneCodeSpan},
		{"double tick", "a ``x ` [read#1]`` b", ZoneCodeSpan},
		{"image alt", "![read#1](pic.png)", ZoneImage},
		{"bracket inside bare url", "see https://example.test/a[read#1] now", ZoneAutolink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			for _, o := range doc.Occurrences {
				if o.Kind == KindCitation || o.Kind == KindVerify {
					t.Fatalf("extracted %+v from %q", o, tc.source)
				}
			}
			if len(doc.Issues) > 0 {
				t.Fatalf("issues = %+v", doc.Issues)
			}
			found := false
			for _, zone := range doc.Zones {
				found = found || zone.Kind == tc.kind
			}
			if !found {
				t.Fatalf("zones = %+v, want a %s", doc.Zones, tc.kind)
			}
			if got := Project(doc); !strings.Contains(got, "read#1") {
				t.Fatalf("presentation dropped the literal: %q", got)
			}
		})
	}
}

func TestLiteralHelpersAreComplements(t *testing.T) {
	t.Parallel()
	source := "a `b` c\n\n```\nd\n```\n\ne [f](g) h"
	zones := LiteralZones(source)
	ranges := TextRanges(source)
	covered := 0
	for _, zone := range zones {
		covered += zone.Span.End - zone.Span.Start
	}
	for _, span := range ranges {
		covered += span.End - span.Start
		if strings.Contains(source[span.Start:span.End], "`") {
			t.Fatalf("text range %q holds code", source[span.Start:span.End])
		}
	}
	if covered != len(source) {
		t.Fatalf("zones and ranges cover %d of %d", covered, len(source))
	}
}

// AC-AST-06: the destination is the one citation; a label never adds another.
func TestParseLinkLabelsAreNotCitations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, source string
		kind         NodeKind
		form         LinkForm
		dest         string
	}{
		{"inline", "[read#3](https://example.test/a)", KindWebLink, LinkInline, "https://example.test/a"},
		{"reference", "[read#3][r]\n\n[r]: https://example.test/b", KindWebLink, LinkReference, "https://example.test/b"},
		{"collapsed", "[read#3][]\n\n[read#3]: https://example.test/c", KindWebLink, LinkReference, "https://example.test/c"},
		{"shortcut", "[read#3]\n\n[read#3]: https://example.test/d", KindWebLink, LinkReference, "https://example.test/d"},
		{"definition before use", "[read#3]: https://example.test/e\n\n[read#3]", KindWebLink, LinkReference, "https://example.test/e"},
		{"nested label", "[see [read#3] now](https://example.test/f)", KindWebLink, LinkInline, "https://example.test/f"},
		{"workspace", "[read#3](internal/a.go#L3)", KindWorkspaceLink, LinkInline, "internal/a.go#L3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			if len(doc.Occurrences) != 1 || len(doc.Issues) != 0 {
				t.Fatalf("occurrences = %+v, issues = %+v", doc.Occurrences, doc.Issues)
			}
			o := doc.Occurrences[0]
			if o.Kind != tc.kind || o.Link.Form != tc.form || o.Link.Destination != tc.dest {
				t.Fatalf("occurrence = %+v, link = %+v", o, o.Link)
			}
		})
	}
}

// AC-AST-07: every HTTP(S) destination is a citation; the parser grants nothing.
func TestParseWebLinks(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, source, dest, label string }{
		{"named", "[RFC 9110](https://example.test/spec)", "https://example.test/spec", "RFC 9110"},
		{"autolink", "<https://example.test/spec>", "https://example.test/spec", "https://example.test/spec"},
		{"bare url", "see https://example.test/spec now", "https://example.test/spec", "https://example.test/spec"},
		{"www", "see www.example.test/spec now", "http://www.example.test/spec", "www.example.test/spec"},
		{"entity", "[a](https://example.test/?a=1&amp;b=2)", "https://example.test/?a=1&b=2", "a"},
		{"upper scheme", "[a](HTTPS://example.test)", "HTTPS://example.test", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, Options{})
			if len(doc.Occurrences) != 1 {
				t.Fatalf("occurrences = %+v", doc.Occurrences)
			}
			o := doc.Occurrences[0]
			if o.Kind != KindWebLink || o.Link.Destination != tc.dest || o.Link.Label != tc.label {
				t.Fatalf("occurrence = %+v, link = %+v", o, o.Link)
			}
			if tc.source[o.Link.LabelSpan.Start:o.Link.LabelSpan.End] != tc.label {
				t.Fatalf("label span %v", o.Link.LabelSpan)
			}
		})
	}
	for _, source := range []string{"[a](file:///tmp/a.go)", "[a](mailto:x@example.test)", "[a](#top)", "<ftp://example.test/a>", "<x@example.test>", "[a]()"} {
		if doc := Parse(source, Options{}); len(doc.Occurrences) != 0 {
			t.Fatalf("%q yielded %+v", source, doc.Occurrences)
		}
	}
}

// AC-AST-09: every separator yields the canonical assessment; the receipt is a
// separate citation.
func TestParseVerifyDirective(t *testing.T) {
	t.Parallel()
	for _, sep := range []string{" - ", "-", " -- ", "--", " — ", "—", " – ", "–", ": ", ":", " : "} {
		for _, method := range []string{verification.Inspection, verification.Targeted, verification.Project, verification.Blocked} {
			source := "Done. [verify: " + method + sep + "checked auth [pkg]] [command#4]"
			doc := Parse(source, Options{})
			if len(doc.Issues) != 0 || len(doc.Occurrences) != 2 {
				t.Fatalf("%q: occurrences = %+v, issues = %+v", source, doc.Occurrences, doc.Issues)
			}
			got := doc.Occurrences[0]
			want := verification.Assessment{Method: method, Reason: "checked auth [pkg]"}
			if got.Kind != KindVerify || *got.Verify != want {
				t.Fatalf("%q: verify = %+v", source, got.Verify)
			}
			if receipt := doc.Occurrences[1]; receipt.Citation == nil || receipt.Citation.Handle != "command#4" {
				t.Fatalf("%q: receipt = %+v", source, receipt)
			}
		}
	}
}
