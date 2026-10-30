package secretmatch

import (
	"context"
	"io"
	"strings"
	"unicode/utf8"
)

func (s *streamScreen) flush(ctx context.Context, dst io.Writer, raw string, mask referenceTokenMask, cutoff int64) error {
	count := 0
	lineParts := int64(0)
	for _, hit := range s.hits.values {
		if hit.start >= s.emit && hit.end <= cutoff && hit.start >= s.inputOrigin {
			count++
			covered := raw[int(hit.start-s.base):int(hit.end-s.base)]
			lineParts += int64(strings.Count(covered, "\n") + strings.Count(covered, "\r"))
		}
	}
	parts := int64(count) * int64(len(mask.spans)+1)
	charge := (parts+lineParts)*2048 + int64(count)*512
	if count > 0 {
		prefix := raw[int(s.emit-s.base):int(cutoff-s.base)]
		charge += int64(len(prefix)) * 8
	}
	if err := s.memory.take(charge); err != nil {
		return err
	}
	defer s.memory.release(charge)
	hits := make([]Match, 0, count)
	for _, v := range s.hits.values {
		if v.start < s.emit || v.end > cutoff || v.start < s.inputOrigin {
			continue
		}
		hit := v.hit
		hit.Start = utf8.RuneCountInString(raw[:int(v.start-s.base)])
		hit.End = utf8.RuneCountInString(raw[:int(v.end-s.base)])
		hits = append(hits, hit)
	}
	hits = mask.outside(hits)
	work := int64(len(hits)) * int64(len(hits)) * 2
	if work > s.bounds.DetectorWork {
		return &StreamLimitError{"screen_work_units", s.bounds.DetectorWork, work}
	}
	if err := propagateNonDisclosable(hits, ctx.Err); err != nil {
		return err
	}
	var err error
	hits, err = mergeContainerEvidence(hits, ctx.Err)
	if err != nil {
		return err
	}
	hits = s.matcher.dropIgnored(ctx, hits)
	begin, end := int(s.emit-s.base), int(cutoff-s.base)
	prefix := raw[begin:end]
	runeStart := utf8.RuneCountInString(raw[:begin])
	for i := range hits {
		hits[i].Start -= runeStart
		hits[i].End -= runeStart
	}
	safe := prefix
	var replacements []Replacement
	if len(hits) > 0 {
		safe, replacements = rewriteMatches(prefix, hits, false)
	}
	outputStart := s.result.OutputBytes
	if err := s.write(ctx, dst, safe); err != nil {
		return err
	}
	if len(replacements) > 0 {
		s.result.Redacted = true
	}
	if s.observe != nil {
		for _, r := range replacements {
			in := MatchByteSpans(prefix, []Match{{Start: r.SourceStart, End: r.SourceEnd}})
			out := MatchByteSpans(safe, []Match{{Start: r.Start, End: r.Start + r.Length}})
			if len(in) == 1 && len(out) == 1 {
				if err := s.observe(ctx, StreamSpan{InputStart: s.emit - s.inputOrigin + int64(in[0].Start), InputEnd: s.emit - s.inputOrigin + int64(in[0].End), OutputStart: outputStart + int64(out[0].Start), OutputEnd: outputStart + int64(out[0].End)}); err != nil {
					return err
				}
			}
		}
	}
	s.emit = cutoff
	next := s.hits.values[:0]
	for _, hit := range s.hits.values {
		if hit.end > cutoff {
			next = append(next, hit)
		}
	}
	clear(s.hits.values[len(next):])
	s.hits.values = next
	return nil
}

func (s *streamScreen) write(ctx context.Context, dst io.Writer, value string) error {
	if err := s.memory.take(2 * streamReadBytes); err != nil {
		return err
	}
	defer s.memory.release(2 * streamReadBytes)
	for len(value) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.check != nil {
			if err := s.check(); err != nil {
				return err
			}
		}
		end := min(len(value), streamReadBytes)
		observed := s.result.OutputBytes + int64(end)
		if observed > s.bounds.OutputBytes {
			return &StreamLimitError{"screen_output_bytes", s.bounds.OutputBytes, observed}
		}
		n, err := io.WriteString(dst, value[:end])
		if n < 0 || n > end {
			return io.ErrShortWrite
		}
		s.result.OutputBytes += int64(n)
		if err != nil {
			return err
		}
		if n != end {
			return io.ErrShortWrite
		}
		value = value[end:]
	}
	return nil
}
