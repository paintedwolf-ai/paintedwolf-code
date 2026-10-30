package textsearch

import (
	"bytes"
	"context"
	"errors"
)

// LineKind separates selected records from context around them.
type LineKind int

const (
	SelectedLine LineKind = iota + 1
	ContextLine
)

// Line is one record of the scanned content. Start and End are byte offsets
// into the content; End excludes the LF (and a trimmed CR).
type Line struct {
	Number     int
	Start, End int
	Kind       LineKind
	// Gap reports that a non-adjacent group starts here: an earlier line was
	// yielded and context is in use (grep prints "--").
	Gap bool
	// Span is the selector's first match within the record for a selected,
	// non-inverted line.
	Span Span
}

// ScanOptions are grep's record-selection controls.
type ScanOptions struct {
	Invert bool
	// MaxSelected stops after that many selected lines (grep -m); trailing
	// context is still yielded. Negative means unlimited, zero reads nothing.
	MaxSelected   int
	Before, After int
	// TrimCR drops one CR before each LF from the matched record.
	TrimCR bool
	// NullData separates records with NUL instead of LF (grep -z).
	NullData bool
}

// ErrStopScan ends a scan early without error when a yield returns it.
var ErrStopScan = errors.New("stop line scan")

// ScanLines selects records of content with sel and yields selected and
// context lines in order. It returns how many lines were selected. A record
// error (pattern budget, cancellation) stops the scan and is returned.
func ScanLines(ctx context.Context, content []byte, sel Selector, opts ScanOptions, yield func(Line) error) (int, error) {
	s := lineScan{opts: opts, yield: yield}
	if opts.MaxSelected == 0 {
		return 0, nil
	}
	trailing := false
	separator := byte('\n')
	if opts.NullData {
		separator = 0
	}
	for start, number := 0, 1; start < len(content); number++ {
		if err := ctx.Err(); err != nil {
			return s.selected, err
		}
		end := len(content)
		if i := bytes.IndexByte(content[start:], separator); i >= 0 {
			end = start + i
		}
		next := end + 1
		recordEnd := end
		if opts.TrimCR && !opts.NullData && recordEnd > start && content[recordEnd-1] == '\r' {
			recordEnd--
		}
		line := Line{Number: number, Start: start, End: recordEnd}
		start = next
		if trailing {
			if s.after == 0 {
				break
			}
			if err := s.context(line); err != nil {
				return s.finish(err)
			}
			continue
		}
		span, matched, err := sel.Select(ctx, content[line.Start:line.End])
		if err != nil {
			return s.selected, err
		}
		if matched == opts.Invert {
			if err := s.unselected(line); err != nil {
				return s.finish(err)
			}
			continue
		}
		if matched {
			line.Span = span
		}
		if err := s.selectedLine(line); err != nil {
			return s.finish(err)
		}
		trailing = opts.MaxSelected > 0 && s.selected >= opts.MaxSelected
	}
	return s.selected, nil
}

type lineScan struct {
	opts     ScanOptions
	yield    func(Line) error
	selected int
	last     int
	after    int
	pending  []Line
}

func (s *lineScan) finish(err error) (int, error) {
	if errors.Is(err, ErrStopScan) {
		return s.selected, nil
	}
	return s.selected, err
}

func (s *lineScan) emit(line Line) error {
	line.Gap = s.last > 0 && line.Number > s.last+1 && (s.opts.Before > 0 || s.opts.After > 0)
	s.last = line.Number
	return s.yield(line)
}

func (s *lineScan) context(line Line) error {
	s.after--
	line.Kind = ContextLine
	return s.emit(line)
}

func (s *lineScan) unselected(line Line) error {
	if s.after > 0 {
		return s.context(line)
	}
	if s.opts.Before > 0 {
		line.Kind = ContextLine
		s.pending = append(s.pending, line)
		if len(s.pending) > s.opts.Before {
			s.pending = s.pending[1:]
		}
	}
	return nil
}

func (s *lineScan) selectedLine(line Line) error {
	for _, before := range s.pending {
		if err := s.emit(before); err != nil {
			return err
		}
	}
	s.pending = s.pending[:0]
	line.Kind = SelectedLine
	s.selected++
	s.after = s.opts.After
	return s.emit(line)
}
