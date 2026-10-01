package sourcecomparison

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"unsafe"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
)

// Fold addresses immutable source rows. Display ranks belong to one projection.
type Fold struct{ Start, End int }
type displaySpan struct {
	start, end int
	collapsed  bool
}
type Projection struct {
	document *Document
	index    pagedview.RangeIndex[displaySpan]
	spans    []displaySpan
	pages    *pagedview.MemoryPages[displaySpan]
	mode     string
	screens  *projectionScreens
}

type projectionScreens struct{ before, after *api.SecretScreen }

// WithSecretScreens copies the projection without changing existing presentations.
func (p *Projection) WithSecretScreens(before, after *api.SecretScreen) *Projection {
	next := *p
	next.screens = &projectionScreens{
		before: normalizedScreen(before, p.document.Before.Content),
		after:  normalizedScreen(after, p.document.After.Content),
	}
	return &next
}

type ProjectedRow struct {
	Rank   int64
	Source api.SourceReaderRow
}
type ComparisonAnchor struct{ Row int }

var ErrMode = errors.New("unknown comparison display mode")

func (d *Document) Project(ctx context.Context, mode string, expanded []Fold, reserve func(int64) error) (*Projection, error) {
	switch mode {
	case "full", "changes", "split", "before", "after":
	default:
		return nil, ErrMode
	}
	d.prepare()
	folds := append([]Fold(nil), expanded...)
	sort.Slice(folds, func(i, j int) bool { return folds[i].Start < folds[j].Start })
	for _, fold := range folds {
		if fold.Start < 0 || fold.End <= fold.Start || fold.End > len(d.rows) {
			return nil, pagedview.ErrRange
		}
	}
	count := 0
	if err := d.walkDisplaySpans(ctx, mode, folds, func(displaySpan) { count++ }); err != nil {
		return nil, err
	}
	// The reservation covers retained pages and the largest merge batch.
	if reserve != nil {
		if err := reserve(int64(count)*320 + 64<<10); err != nil {
			return nil, err
		}
	}
	pages := &pagedview.MemoryPages[displaySpan]{}
	p := &Projection{document: d, mode: mode, pages: pages, spans: make([]displaySpan, 0, count), index: pagedview.RangeIndex[displaySpan]{Store: pages}}
	if err := d.walkDisplaySpans(ctx, mode, folds, func(span displaySpan) { p.spans = append(p.spans, span) }); err != nil {
		return nil, err
	}
	// Spans retain coordinates; Frame materializes row details.
	for offset := 0; offset < len(p.spans); offset += pagedview.PageFanout {
		spans := p.spans[offset:min(len(p.spans), offset+pagedview.PageFanout)]
		batch := make([]pagedview.RangeItem[displaySpan], len(spans))
		for i, span := range spans {
			weight := int64(span.end - span.start)
			if span.collapsed {
				weight = 1
			}
			batch[i] = pagedview.RangeItem[displaySpan]{Key: comparisonSpanKey(span.start), Value: span, Weight: weight}
		}
		if err := p.index.SetBatch(ctx, batch); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// DisplayExtent is a projection's extent without building it; Folds counts
// collapsed runs within Rows.
type DisplayExtent struct{ Rows, Folds int }

func (d *Document) DisplayExtent(ctx context.Context, mode string) (DisplayExtent, error) {
	switch mode {
	case "full", "changes", "split", "before", "after":
	default:
		return DisplayExtent{}, ErrMode
	}
	d.prepare()
	var extent DisplayExtent
	err := d.walkDisplaySpans(ctx, mode, nil, func(span displaySpan) {
		if span.collapsed {
			extent.Rows++
			extent.Folds++
			return
		}
		extent.Rows += span.end - span.start
	})
	return extent, err
}

// walkDisplaySpans emits compact display runs without retaining a second plan.
func (d *Document) walkDisplaySpans(ctx context.Context, mode string, folds []Fold, emit func(displaySpan)) error {
	foldIndex, foldEnd := 0, 0
	var held displaySpan
	for at, row := range d.rows {
		if at%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for foldIndex < len(folds) && folds[foldIndex].Start <= at {
			foldEnd = max(foldEnd, folds[foldIndex].End)
			foldIndex++
		}
		if mode == "before" && row.BeforeLine == 0 || mode == "after" && row.AfterLine == 0 || mode == "split" && row.Kind == "insert" && row.peer >= 0 {
			continue
		}
		collapsed := (mode == "changes" || mode == "split") && !row.visible && at >= foldEnd
		if held.end > held.start && held.end == at && held.collapsed == collapsed {
			held.end++
			continue
		}
		if held.end > held.start {
			emit(held)
		}
		held = displaySpan{start: at, end: at + 1, collapsed: collapsed}
	}
	if held.end > held.start {
		emit(held)
	}
	return ctx.Err()
}

func (p *Projection) RetainedBytes() int64 {
	if p == nil {
		return 0
	}
	bytes := int64(unsafe.Sizeof(*p)) + int64(cap(p.spans))*int64(unsafe.Sizeof(displaySpan{})) + p.pages.RetainedBytes()
	if p.screens != nil {
		for _, screen := range []*api.SecretScreen{p.screens.before, p.screens.after} {
			if screen != nil {
				bytes += int64(unsafe.Sizeof(*screen)) + int64(cap(screen.Spans))*128
			}
		}
	}
	return bytes
}

func comparisonSpanKey(index int) string                        { return fmt.Sprintf("%016x", index) }
func (p *Projection) Extent(ctx context.Context) (int64, error) { return p.index.Extent(ctx) }

func (p *Projection) Locate(ctx context.Context, sourceRow int) (int64, ComparisonAnchor, error) {
	if sourceRow < 0 || sourceRow >= len(p.document.rows) {
		return 0, ComparisonAnchor{}, pagedview.ErrRange
	}
	if p.mode == "split" {
		sourceRow = p.document.DisplayRow(sourceRow)
	}
	if len(p.spans) == 0 {
		return 0, ComparisonAnchor{}, pagedview.ErrRange
	}
	at := sort.Search(len(p.spans), func(i int) bool { return p.spans[i].end > sourceRow })
	if at == len(p.spans) {
		at--
	}
	span := p.spans[at]
	_, rank, err := p.index.Locate(ctx, comparisonSpanKey(span.start))
	row := max(span.start, min(sourceRow, span.end-1))
	if span.collapsed {
		row = span.start
	} else {
		rank += int64(row - span.start)
	}
	return rank, ComparisonAnchor{Row: row}, err
}

func (p *Projection) Frame(ctx context.Context, offset int64, limit int) ([]ProjectedRow, int64, error) {
	if offset < 0 || limit < 1 || limit > pagedview.MaxRows {
		return nil, 0, pagedview.ErrRange
	}
	total, err := p.Extent(ctx)
	if err != nil {
		return nil, 0, err
	}
	if offset > total {
		return nil, 0, pagedview.ErrRange
	}
	if err := p.document.Decorate(ctx); err != nil {
		return nil, 0, err
	}
	rows := make([]ProjectedRow, 0, limit)
	for rank := offset; rank < total && len(rows) < limit; {
		item, within, err := p.index.Select(ctx, rank)
		if err != nil {
			return nil, 0, err
		}
		span := item.Value
		if span.collapsed {
			row := p.document.row(span.start)
			row.Kind, row.End, row.Text = "gap", span.end, ""
			rows = append(rows, ProjectedRow{Rank: rank, Source: row})
			rank++
			continue
		}
		for index := span.start + int(within); index < span.end && len(rows) < limit; index++ {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			row := p.document.materialize(index, p.screens)
			if peer := p.document.rows[index].peer; p.mode == "split" && peer >= 0 {
				counterpart := p.document.materialize(peer, p.screens)
				row.Peer = &counterpart
			}
			rows = append(rows, ProjectedRow{Rank: rank, Source: row})
			rank++
		}
	}
	return rows, total, nil
}
