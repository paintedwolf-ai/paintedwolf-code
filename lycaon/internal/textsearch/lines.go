package textsearch

import (
	"bufio"
	"context"
	"errors"
	"io"
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
	Text       []byte
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

// StopScan ends a scan early without error when a yield returns it.
var StopScan = errors.New("stop line scan")

// ScanReader selects records from r with sel and yields selected and context
// lines in order without materializing the entire input in memory. If maxBytes
// is positive and the stream exceeds maxBytes before selection completes,
// onExceed(maxBytes, observed) is returned.
func ScanReader(ctx context.Context, r io.Reader, maxBytes int64, onExceed func(maximum, observed int64) error, sel Selector, opts ScanOptions, yield func(Line) error) (int, error) {
	s := lineScan{opts: opts, yield: yield}
	if opts.MaxSelected == 0 {
		return 0, nil
	}
	trailing := false
	separator := byte('\n')
	if opts.NullData {
		separator = 0
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for start, number := 0, 1; ; number++ {
		if err := ctx.Err(); err != nil {
			return s.selected, err
		}
		if trailing && s.after == 0 {
			break
		}
		var raw []byte
		var readErr error
		for {
			if err := ctx.Err(); err != nil {
				return s.selected, err
			}
			chunk, err := br.ReadSlice(separator)
			raw = append(raw, chunk...)
			if maxBytes > 0 && int64(start+len(raw)) > maxBytes {
				return s.selected, onExceed(maxBytes, int64(start+len(raw)))
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			readErr = err
			break
		}
		if len(raw) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return s.selected, readErr
		}
		next := start + len(raw)
		text := raw
		if len(text) > 0 && text[len(text)-1] == separator {
			text = text[:len(text)-1]
		}
		if opts.TrimCR && !opts.NullData && len(text) > 0 && text[len(text)-1] == '\r' {
			text = text[:len(text)-1]
		}
		line := Line{Number: number, Start: start, End: start + len(text), Text: text}
		start = next
		if trailing {
			if err := s.context(line); err != nil {
				return s.finish(err)
			}
			if s.after == 0 || errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		span, matched, err := sel.Select(ctx, line.Text)
		if err != nil {
			return s.selected, err
		}
		if matched == opts.Invert {
			if err := s.unselected(line); err != nil {
				return s.finish(err)
			}
			if errors.Is(readErr, io.EOF) {
				break
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
		if (trailing && s.after == 0) || errors.Is(readErr, io.EOF) {
			break
		}
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
	if errors.Is(err, StopScan) {
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
