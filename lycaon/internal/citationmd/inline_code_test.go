package citationmd

import "testing"

func codeOptions(known ...string) Options {
	return Options{
		KnownPath: func(name string) bool {
			for _, path := range known {
				if path == name {
					return true
				}
			}
			return false
		},
		CodeCitation: func(c Citation) bool { return c.Line <= 30 },
	}
}

func TestStandaloneCodeSpanCitation(t *testing.T) {
	t.Parallel()
	const path = "cmd/gateway/main.go"
	for _, tc := range []struct {
		name, source string
		want         *Citation
	}{
		{"a line", "The deadline is set at `cmd/gateway/main.go:21`.", &Citation{Path: path, Line: 21}},
		{"a range", "(`cmd/gateway/main.go:21-23`)", &Citation{Path: path, Line: 21, EndLine: 23}},
		{"table cell", "| `cmd/gateway/main.go:2` | x |", &Citation{Path: path, Line: 2}},
		{"emphasised", "**`cmd/gateway/main.go:2`**", &Citation{Path: path, Line: 2}},
		{"unsupported coordinate stays code", "See `cmd/gateway/main.go:31`.", nil},
		{"unknown path stays code", "See `cmd/other.go:2`.", nil},
		{"host and port stays code", "Dial `localhost:8080`.", nil},
		{"no coordinate stays code", "See `cmd/gateway/main.go`.", nil},
		{"handle stays code", "Write `read#1:2`.", nil},
		{"bracket form stays code", "Write `[cmd/gateway/main.go:2]`.", nil},
		{"more than the coordinate stays code", "See `cmd/gateway/main.go:2 here`.", nil},
		{"inner padding stays code", "See ` cmd/gateway/main.go:2 `.", nil},
		{"double backticks stay code", "See ``cmd/gateway/main.go:2``.", nil},
		{"word before stays code", "at`cmd/gateway/main.go:2`.", nil},
		{"word after stays code", "`cmd/gateway/main.go:2`x", nil},
		{"fence stays code", "```\n`cmd/gateway/main.go:2`\n```\n", nil},
		{"link label stays a label", "[`cmd/gateway/main.go:2`](https://example.test/x)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, codeOptions(path))
			if len(doc.Issues) != 0 {
				t.Fatalf("code span raised issues: %+v", doc.Issues)
			}
			var got *Occurrence
			for i := range doc.Occurrences {
				if doc.Occurrences[i].InlineCode {
					got = &doc.Occurrences[i]
				}
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got citation %+v", got.Citation)
			case tc.want != nil && (got == nil || *got.Citation != *tc.want):
				t.Fatalf("occurrences = %+v, want %+v", doc.Occurrences, tc.want)
			}
		})
	}
}

// Without the evidence's say-so, the span is code to every consumer.
func TestCodeSpanNeverVanishes(t *testing.T) {
	t.Parallel()
	const source = "The cap is at `a.go:2` today."
	doc := Parse(source, codeOptions("a.go"))
	if len(doc.Occurrences) != 1 || doc.Occurrences[0].Kind != KindCitation {
		t.Fatalf("occurrences = %+v", doc.Occurrences)
	}
	if got := Render(doc, func(Occurrence) Rendition { return Rendition{} }); got != source {
		t.Fatalf("unaccepted span rendered %q", got)
	}
	if got := Narrative(doc); got != source {
		t.Fatalf("narrative = %q", got)
	}
	if got := Render(doc, func(Occurrence) Rendition { return Rendition{Text: FileLink("a.go", 2, 0)} }); got != "The cap is at [a.go:2](a.go#L2) today." {
		t.Fatalf("accepted span rendered %q", got)
	}
	if plain := Parse(source, Options{}); len(plain.Occurrences) != 0 {
		t.Fatalf("a parse with no evidence took the span: %+v", plain.Occurrences)
	}
}

func TestLinePrefixedCoordinates(t *testing.T) {
	t.Parallel()
	const path = "demo_win.py"
	opts := codeOptions(path)
	opts.KnownHandleKind = func(kind string) bool { return kind == "read" }
	for _, tc := range []struct {
		name, source string
		want         Citation
	}{
		{"spaced line coordinate", "Simulated in [demo_win.py:line 57].", Citation{Path: path, Line: 57}},
		{"compact line coordinate", "Simulated in [demo_win.py:line57].", Citation{Path: path, Line: 57}},
		{"spaced line range", "Simulated in [demo_win.py:line 10-20].", Citation{Path: path, Line: 10, EndLine: 20}},
		{"repeated line range", "Simulated in [demo_win.py:line 10-line 20].", Citation{Path: path, Line: 10, EndLine: 20}},
		{"handle line coordinate", "Simulated in [read#1:line 5].", Citation{Handle: "read#1", Line: 5}},
		{"code span line coordinate", "Simulated in `demo_win.py:line 21`.", Citation{Path: path, Line: 21}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, opts)
			if len(doc.Issues) != 0 || len(doc.Occurrences) != 1 || doc.Occurrences[0].Citation == nil || *doc.Occurrences[0].Citation != tc.want {
				t.Fatalf("occurrences = %+v, issues = %+v, want %+v", doc.Occurrences, doc.Issues, tc.want)
			}
		})
	}
}
