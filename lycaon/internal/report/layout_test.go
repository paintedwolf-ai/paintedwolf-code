package report

import (
	"strings"
	"testing"
	"time"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
)

// probeProp is the cell treatment these layout probes measure against.
func probeProp() props.Text {
	return props.Text{
		Family:          familySans,
		Size:            sizeTableCell,
		Color:           inkColor,
		VerticalPadding: leading(sizeTableCell),
	}
}

// The renderer draws at an absolute point and clips nothing, so a line wider
// than its column paints over the next one. Every laid-out line must fit.
func TestInlineText_NoLineExceedsMeasure(t *testing.T) {
	ms := testMeasurer(t)
	const width = 28.0

	cases := map[string]string{
		"evidence handle": "da2aefce-7dec-4003-beeb-9a5a033302:grep#24",
		"rule id":         "go.lang.security.audit.net.use-tls.use-tls",
		"deep url":        "https://docs.aws.amazon.com/service-authorization/latest/reference/reference_policies_actions-resources-contextkeys.html",
		"no seam at all":  strings.Repeat("W", 400),
		"ordinary prose":  "short words wrap between themselves without help",
		"single wide run": strings.Repeat("verylongtokenwithoutspaces", 6),
	}

	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			it := newInlineText(ms, []inlineRun{{Text: text, Family: familySans}}, probeProp(), width)
			if len(it.lines) == 0 {
				t.Fatal("want at least one laid-out line")
			}
			for i, ln := range it.lines {
				if ln.width > width+0.01 {
					t.Fatalf("line %d is %.2fmm wide in a %.2fmm column", i, ln.width, width)
				}
			}
			if got := it.plain(); got != text {
				t.Fatalf("layout dropped text:\n got %q\nwant %q", got, text)
			}
		})
	}
}

// A seam break is a join, not damage: an identifier should wrap after one of
// its own separators rather than mid-token, whenever that fits.
func TestInlineText_BreaksIdentifiersAtSeams(t *testing.T) {
	ms := testMeasurer(t)
	const handle = "da2aefce-7dec-4003-beeb-9a5a033302:grep#1"

	it := newInlineText(ms, []inlineRun{{Text: handle, Family: familyMono}}, props.Text{
		Family: familyMono,
		Size:   sizeTableCell,
		Color:  inkColor,
	}, 47.0)

	if len(it.lines) != 2 {
		t.Fatalf("handle laid out in %d lines, want 2", len(it.lines))
	}
	first := lineText(it.lines[0])
	if !strings.HasSuffix(first, "-") {
		t.Fatalf("first line %q: want a break after a seam character", first)
	}
}

func lineText(ln textLine) string {
	var b strings.Builder
	for _, f := range ln.frags {
		b.WriteString(f.text)
	}
	return b.String()
}

// Emphasis applies to the span that carries it, not to the block containing
// it: one ** pair in a bullet must not bold the whole bullet.
func TestMdInline_EmphasisIsPerSpan(t *testing.T) {
	src := []byte("**Medium** — vendoring tracks *branch names*, not `commit pins`.")
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).
		Parser().Parse(gmtext.NewReader(src))

	runs := flattenSegments((&mdRenderer{ms: testMeasurer(t), source: src}).runs(doc))

	var bold, italic, code, plain strings.Builder
	for _, r := range runs {
		switch {
		case r.Bold:
			bold.WriteString(r.Text)
		case r.Italic:
			italic.WriteString(r.Text)
		case r.Family == familyMono:
			code.WriteString(r.Text)
		default:
			plain.WriteString(r.Text)
		}
	}

	if got := bold.String(); got != "Medium" {
		t.Fatalf("bold spans = %q, want just %q", got, "Medium")
	}
	if got := italic.String(); got != "branch names" {
		t.Fatalf("italic spans = %q, want just %q", got, "branch names")
	}
	if got := code.String(); got != "commit pins" {
		t.Fatalf("code spans = %q, want just %q", got, "commit pins")
	}
	if !strings.Contains(plain.String(), "vendoring tracks") {
		t.Fatalf("unemphasised text = %q, want the rest of the sentence", plain.String())
	}
}

// splitLines turns one block into one row per line. The space VerticalPadding
// opened inside the block has to survive that, or prose sets solid and the
// next row's background clips the descenders.
func TestSplitLines_KeepsBlockLeading(t *testing.T) {
	ms := testMeasurer(t)
	prop := probeProp()
	prop.Size = sizeBody
	prop.VerticalPadding = leading(sizeBody)

	text := strings.Repeat("wrapped prose that needs several lines to sit on ", 6)
	blk := newInlineText(ms, []inlineRun{{Text: text, Family: familySans}}, prop, contentWidth)
	if len(blk.lines) < 3 {
		t.Fatalf("fixture only produced %d lines", len(blk.lines))
	}

	var split float64
	for _, ln := range blk.splitLines() {
		split += ln.height()
	}
	if split < blk.height() {
		t.Fatalf("split rows total %.2fmm but the block is %.2fmm: leading was dropped", split, blk.height())
	}

	minBottom := prop.Size * ptToMM * descenderRatio
	for i, ln := range blk.splitLines() {
		if ln.prop.Bottom < minBottom-0.001 {
			t.Fatalf("row %d reserves %.2fmm below its text, want at least %.2fmm", i, ln.prop.Bottom, minBottom)
		}
	}
}

// A table that outgrows a page reprints its column head, so no page of an
// appendix is a wall of unlabelled values.
func TestTable_ReprintsHeaderOnEachNewPage(t *testing.T) {
	ms := testMeasurer(t)

	records := make([]tableRecord, 90)
	for i := range records {
		records[i] = tableRecord{cells: [][]inlineRun{
			{{Text: "row", Family: familySans}},
			{{Text: "lycaon/internal/egressproxy/proxy.go", Family: familySans}},
		}}
	}

	blocks := newTable(ms, []string{"Kind", "Location"}, records, noChipColumn, nil).blocks()

	reprints := 0
	for i := range blocks {
		inner := blocks[i].repeatHeader
		if inner == nil {
			continue
		}
		blocks[i].repeatHeader = func() []measuredRow {
			reprints++
			return inner()
		}
	}

	cfg, err := newConfig(ReportInput{Title: "T"}, mustTime(t, "2026-07-08T15:04:05Z"))
	testutil.FailErr(t, "config", err)

	m := maroto.New(cfg)
	(&docBuilder{m: m}).emit(blocks)
	doc, err := m.Generate()
	testutil.FailErr(t, "generate", err)

	if reprints == 0 {
		t.Fatal("table spanned pages without reprinting its header")
	}
	if len(doc.GetBytes()) == 0 {
		t.Fatal("empty document")
	}
}

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	v, err := parseCompletedAt(raw)
	testutil.FailErr(t, "parse completed_at", err)
	return v
}

// testMeasurer builds a measurer over the embedded faces.
func testMeasurer(t *testing.T) *measurer {
	t.Helper()
	ms, err := newMeasurer()
	testutil.FailErr(t, "new measurer", err)
	return ms
}
