package repomap

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMarkdownDefinitionBlockMatrix(t *testing.T) {
	for _, tc := range []struct{ name, source, heading, span string }{
		{"slash", "### a/b\n", "a/b", "### a/b\n"},
		{"closing hashes", "## Closing ##\n", "Closing", "## Closing ##\n"},
		{"literal hash", "## C#\n", "C#", "## C#\n"},
		{"unicode CRLF", "# café 🐺\r\nbody\r\n", "café 🐺", "# café 🐺\r\n"},
		{"setext", "Guide\n=====\n\nBody\n", "Guide", "Guide\n=====\n"},
		{"multiline setext", "First line\nSecond line\n---\n\nBody\n", "First line\nSecond line", "First line\nSecond line\n---\n"},
		{"blockquote", "> ## Quoted\n", "Quoted", "> ## Quoted\n"},
		{"list", "- ## Listed\n", "Listed", "- ## Listed\n"},
		{"indented code", "    # Code\n\n## Actual\n", "Actual", "## Actual\n"},
		{"long fence", "````md\n```\n# Code\n````\n\n## Actual\n", "Actual", "## Actual\n"},
		{"HTML block", "<div>\n# Code\n</div>\n\n## Actual\n", "Actual", "## Actual\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.source)
			spans, language, supported, err := DefinitionSpans(t.Context(), "markdown", "fixture.md", src)
			testutil.FailErr(t, "extract Markdown definitions", err)
			if language != "markdown" || !supported || len(spans) != 1 || spans[0].Name != tc.heading {
				t.Fatalf("definitions = %+v, language=%q supported=%v", spans, language, supported)
			}
			span := spans[0]
			if got := string(src[span.StartByte:span.EndByte]); got != tc.span {
				t.Fatalf("source span = %q want %q", got, tc.span)
			}
			row := strings.Count(tc.source[:span.StartByte], "\n")
			if span.StartRow != row || span.StartCol != 0 {
				t.Fatalf("source coordinates = %+v", span)
			}
		})
	}
}

func TestMarkdownDefinitionsHandleProseAndTables(t *testing.T) {
	src := []byte("# First\n\n" + strings.Repeat("A paragraph with a [reference](target.md), `inline code`, and ordinary prose.\n\n", 400) +
		"## Last\n\n| Field | Description |\n|---|---|\n" + strings.Repeat("| Value | A description with **emphasis**. |\n", 300))
	out := TagsFromBytes(t.Context(), "guide.md", src)
	if out.Skip.ParseIncomplete != 0 || out.Skip.ParseFailed != 0 || len(out.Definitions) != 2 {
		t.Fatalf("Markdown outline incomplete: skip=%+v definitions=%+v", out.Skip, out.Definitions)
	}
	if out.Definitions[0].Name != "First" || out.Definitions[1].Name != "Last" || out.Definitions[1].StartRow != 802 {
		t.Fatalf("Markdown headings = %+v", out.Definitions)
	}
}

func FuzzMarkdownDefinitionRanges(f *testing.F) {
	for _, seed := range []string{"# Heading\n", "> ## café 🐺\r\n", "first\nsecond\n===\n", "````\n# hidden\n````\n", "\x00\xff\n# after bytes\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64*1024 {
			t.Skip("bounded Markdown fixture")
		}
		src := []byte(source)
		previousEnd := 0
		parsed := markdownTags(src)
		if parsed.failure != nil {
			testutil.FailErr(t, "parse bounded Markdown input", parsed.failure)
		}
		for _, tag := range parsed.tags {
			span, name := tag.Range, tag.NameRange
			if int(span.StartByte) < previousEnd || span.StartByte > span.EndByte || int(span.EndByte) > len(src) ||
				name.StartByte < span.StartByte || name.EndByte > span.EndByte || name.StartByte > name.EndByte {
				t.Fatalf("invalid Markdown source ranges: %+v", tag)
			}
			if tag.Kind != "definition.section" || strings.TrimSpace(tag.Name) == "" {
				t.Fatalf("invalid heading: %+v", tag)
			}
			if row := strings.Count(source[:span.StartByte], "\n"); int(span.StartPoint.Row) != row {
				t.Fatalf("heading line = %d want %d", span.StartPoint.Row, row)
			}
			previousEnd = int(span.EndByte)
		}
	})
}

func TestMarkdownSourceRangeBounds(t *testing.T) {
	for _, tc := range []struct {
		name             string
		lines            []int
		size, start, end int
		valid            bool
	}{
		{"empty source", []int{0}, 0, 0, 0, true},
		{"multiline", []int{0, 3}, 7, 2, 7, true},
		{"negative start", []int{0}, 7, -1, 4, false},
		{"reversed", []int{0}, 7, 5, 4, false},
		{"beyond source", []int{0}, 7, 0, 8, false},
		{"missing lines", nil, 7, 0, 4, false},
		{"missing first line", []int{3}, 7, 0, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			span, valid := markdownSourceRange(tc.lines, tc.size, tc.start, tc.end)
			if valid != tc.valid {
				t.Fatalf("range validity=%v, want %v: %+v", valid, tc.valid, span)
			}
			if valid && (int(span.StartByte) != tc.start || int(span.EndByte) != tc.end) {
				t.Fatalf("byte coordinates changed: %+v", span)
			}
		})
	}
	if strconv.IntSize < 64 {
		return
	}
	maximum := int64(math.MaxUint32)
	limit := int(maximum)
	span, valid := markdownSourceRange([]int{0}, limit, limit, limit)
	if !valid || span.EndByte != math.MaxUint32 || span.EndPoint.Column != math.MaxUint32 {
		t.Fatalf("maximum representable coordinate lost: %+v, %v", span, valid)
	}
	if _, valid := markdownSourceRange([]int{0}, limit+1, 0, limit+1); valid {
		t.Fatal("accepted a byte offset that wraps uint32")
	}
}
