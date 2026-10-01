package sourcecomparison

import (
	"context"
	"unicode/utf8"

	"github.com/lycaon/lycaon/pkg/api"
)

type sourceCoordinate struct{ utf16, runes int }
type decoratedSide struct {
	lines       []string
	coordinates []sourceCoordinate
	syntax      syntaxIndex
	attribution []api.SourceAttributedText
}

func prepareDecoration(ctx context.Context, path, text string, attribution []api.SourceAttributedText) (decoratedSide, error) {
	parts := lines(text)
	syntax, err := syntaxSpans(ctx, path, text)
	if err != nil {
		return decoratedSide{}, err
	}
	plan := decoratedSide{lines: parts, coordinates: make([]sourceCoordinate, len(parts)), syntax: syntax, attribution: attribution}
	var at sourceCoordinate
	for i, line := range parts {
		plan.coordinates[i] = at
		at.utf16 += width(line)
		at.runes += utf8.RuneCountInString(line)
	}
	return plan, ctx.Err()
}

type decorationPlans struct{ before, after decoratedSide }

// Decorate shares cancelable preparation before a view starts serving rows.
func (d *Document) Decorate(ctx context.Context) error {
	d.decorationMu.Lock()
	ready := d.decorated
	d.decorationMu.Unlock()
	if ready {
		return ctx.Err()
	}
	plans, err := d.decorationWork.Do(ctx, true, func(work context.Context) (decorationPlans, error) {
		d.decorationMu.Lock()
		if d.decorated {
			plans := decorationPlans{d.beforePlan, d.afterPlan}
			d.decorationMu.Unlock()
			return plans, nil
		}
		d.decorationMu.Unlock()
		var before, after []api.SourceAttributedText
		if d.Attribution != nil {
			before, after = d.Attribution.Before, d.Attribution.After
		}
		a, err := prepareDecoration(work, d.Before.Path, d.beforeText, before)
		if err != nil {
			return decorationPlans{}, err
		}
		b, err := prepareDecoration(work, d.After.Path, d.afterText, after)
		return decorationPlans{a, b}, err
	})
	if err != nil {
		return err
	}
	d.decorationMu.Lock()
	if !d.decorated {
		d.beforePlan, d.afterPlan, d.decorated = plans.before, plans.after, true
	}
	d.decorationMu.Unlock()
	return nil
}

func (d *Document) materialize(index int, screens *projectionScreens) api.SourceReaderRow {
	row := d.row(index)
	row.Changed = d.changed(index)
	side, line := &d.afterPlan, row.AfterLine
	if row.AfterLine == 0 {
		side, line = &d.beforePlan, row.BeforeLine
	}
	if line <= 0 || line > len(side.coordinates) {
		return row
	}
	at := side.coordinates[line-1]
	columnRunes := 0
	units := 0
	for _, r := range side.lines[line-1] {
		if units >= row.Column {
			break
		}
		columnRunes++
		units++
		if r > 0xffff {
			units++
		}
	}
	at.utf16 += row.Column
	at.runes += columnRunes
	row.Syntax = rowSyntax(side.syntax, at.utf16, width(row.Text))
	var screen *api.SecretScreen
	if screens != nil {
		screen = screens.after
		if row.AfterLine == 0 {
			screen = screens.before
		}
	}
	row.SecretScreen = screenRange(screen, at.runes, utf8.RuneCountInString(row.Text))
	row.Contributors = authors(side.attribution, at.utf16, width(row.Text))
	return row
}
