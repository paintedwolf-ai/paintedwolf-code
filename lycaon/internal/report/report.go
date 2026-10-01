package report

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/phpdave11/gofpdf"
)

// PDFProducer is the fixed PDF Creator/Author metadata string.
// Pinning it keeps golden bytes stable across environments.
const PDFProducer = "painted-wolf-report"

var enableCatalogSortOnce sync.Once

// Render turns ReportInput into a deterministic, byte-identical PDF. Pure Go; no network I/O.
func Render(input ReportInput) ([]byte, error) {
	enableCatalogSortOnce.Do(func() {
		gofpdf.SetDefaultCatalogSort(true)
	})

	createdAt, err := parseCompletedAt(input.CompletedAt)
	if err != nil {
		return nil, err
	}
	// maroto sets CreationDate from config but leaves ModDate as wall-clock;
	// pin the gofpdf default so both timestamps match CompletedAt.
	gofpdf.SetDefaultModificationDate(createdAt)

	ms, err := newMeasurer()
	if err != nil {
		return nil, err
	}
	cfg, err := newConfig(input, createdAt)
	if err != nil {
		return nil, err
	}

	m := maroto.New(cfg)
	d := &docBuilder{m: m}
	d.emit(layoutBlocks(ms, cfg, input))

	doc, err := m.Generate()
	if err != nil {
		return nil, fmt.Errorf("generate pdf: %w", err)
	}
	return doc.GetBytes(), nil
}

func newConfig(input ReportInput, createdAt time.Time) (*entity.Config, error) {
	fonts, err := customFonts()
	if err != nil {
		return nil, err
	}
	return config.NewBuilder().
		WithCreator(PDFProducer, true).
		WithAuthor(PDFProducer, true).
		WithTitle(documentTitle(input), true).
		WithCreationDate(createdAt).
		WithSequentialMode().
		WithCompression(true).
		WithCustomFonts(fonts).
		WithDefaultFont(&props.Font{Family: familySans, Size: sizeBody, Color: inkColor}).
		WithMaxGridSize(gridSize).
		WithLeftMargin(marginLeft).
		WithRightMargin(marginRight).
		WithTopMargin(marginTop).
		WithBottomMargin(marginBottom).
		WithPageNumber(props.PageNumber{
			Pattern: runningFooter(input),
			Place:   props.LeftBottom,
			Family:  familySans,
			Size:    sizeMeta,
			Color:   mutedColor,
		}).
		Build(), nil
}

// documentTitle names the file in a viewer's title bar: the workflow or
// session plus its project, never the headline, which is a claim not a name.
func documentTitle(input ReportInput) string {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "Report"
	}
	if project := strings.TrimSpace(input.Project); project != "" {
		return title + " · " + project
	}
	return title
}

// runningFooter is the line every page carries: what this is, and where the
// reader is in it. The library fills the page numbers.
func runningFooter(input ReportInput) string {
	parts := []string{documentTitle(input)}
	if id := shortID(input.RunID); id != "" {
		parts = append(parts, "run "+id)
	}
	return strings.Join(parts, " · ") + "   ·   page {current} of {total}"
}

func parseCompletedAt(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("completed_at: empty")
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("completed_at %q: %w", raw, err)
	}
	return t.UTC(), nil
}

// layoutBlocks decides the whole document, contents included. The body is
// paginated once against a contents placeholder; when the result runs long
// enough to need a map, the placeholder is replaced with the real entries and
// the same pagination holds, since the placeholder had the same height.
func layoutBlocks(ms *measurer, cfg *entity.Config, input ReportInput) []block {
	front, body := buildBlocks(ms, input)
	placeholder := contentsBlocks(ms, placeholderEntries(body))
	candidate := join(front, placeholder, body)

	dry := &docBuilder{m: maroto.New(cfg)}
	dry.emit(candidate)
	// The brief is its own page and needs no map; only what follows it counts.
	if dry.page-briefPages < minPagesForContents {
		return join(front, nil, body)
	}
	pages := dry.pages[len(front)+len(placeholder):]
	return join(front, contentsBlocks(ms, contentsEntries(body, pages)), body)
}

// placeholderEntries are the body's anchors with no page yet, so the contents
// can be sized before anything is paginated.
func placeholderEntries(body []block) []contentsEntry {
	return contentsEntries(body, nil)
}

func join(front, contents, body []block) []block {
	out := make([]block, 0, len(front)+len(contents)+len(body))
	out = append(out, front...)
	out = append(out, contents...)
	return append(out, body...)
}

// buildBlocks lays the report out as the brief and the working summary, then
// the body, conclusions first and the machine record last. Presence is decided
// by data alone, never by workflow identity.
func buildBlocks(ms *measurer, input ReportInput) (front, body []block) {
	renders, captures := artifactSectionBlocks(ms, input.Artifacts)

	front = briefBlocks(ms, input)
	front = append(front, summaryBlocks(ms, input)...)

	if len(input.Findings) > 0 {
		body = append(body, findingsBlocks(ms, input)...)
	}
	if strings.TrimSpace(input.Synthesis) != "" {
		body = append(body, sectionTitle(ms, sectionAssessment))
		body = append(body, mdToBlocks(ms, input.Synthesis)...)
	}
	if len(input.Verdicts) > 0 {
		body = append(body, adjudicationBlocks(ms, input.Verdicts)...)
	}
	if input.Scan != nil || len(input.ScanRows) > 0 {
		body = append(body, scanBlocks(ms, input.ScanRows, input.ScanRules, input.Scan)...)
	}
	if len(renders) > 0 {
		body = append(body, visualsBlocks(ms, renders)...)
	}
	if len(input.Evidence) > 0 || len(captures) > 0 {
		body = append(body, evidenceAppendixBlocks(ms, input.Evidence, input.EvidenceTotal, captures)...)
	}
	if len(input.Sources) > 0 {
		body = append(body, sourcesBlocks(ms, input.Sources)...)
	}
	body = append(body, colophonBlocks(ms, input)...)
	return front, body
}
