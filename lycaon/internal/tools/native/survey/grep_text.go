package survey

import (
	"bytes"

	"github.com/lycaon/lycaon/internal/textsearch"
)

// Pagination discards skipped matches without retaining them.
func (s *grepSearch) scanPaginatedText(rel string, content []byte, bytesTruncated bool) error {
	s.textScanned++
	if bytesTruncated {
		s.filesBytesTruncated++
	}
	var consumeErr error
	err := s.forEachTextMatch(rel, content, func(match grepMatch) bool {
		consumeErr = s.acceptMatch(match)
		return consumeErr == nil
	})
	if err != nil {
		return err
	}
	return consumeErr
}

func (s *grepSearch) collectMatches(rel string, content []byte) ([]grepMatch, error) {
	var matches []grepMatch
	err := s.forEachTextMatch(rel, content, func(match grepMatch) bool {
		matches = append(matches, match)
		return s.maxMatches <= 0 || len(matches) < s.offset+s.maxMatches
	})
	return matches, err
}

// forEachTextMatch runs the shared line scan with the tool's RE2 contract.
// Only matching lines allocate surrounding context.
func (s *grepSearch) forEachTextMatch(rel string, content []byte, yield func(grepMatch) bool) error {
	_, err := textsearch.ScanLines(s.ctx, content, textsearch.RegexpSelector{Re: s.re}, textsearch.ScanOptions{MaxSelected: -1, TrimCR: true}, func(line textsearch.Line) error {
		record := content[line.Start:line.End]
		entry := grepMatch{Path: rel, Line: line.Number, Content: string(record), Match: string(record[line.Span.Start:line.Span.End])}
		if s.contextLines > 0 {
			entry.ContextBefore, entry.ContextAfter = grepLineContext(content, line.Start, grepLineEnd(content, line.Start), s.contextLines)
		}
		if !yield(entry) {
			return textsearch.ErrStopScan
		}
		return nil
	})
	return err
}

func grepLineEnd(content []byte, start int) int {
	if n := bytes.IndexByte(content[start:], '\n'); n >= 0 {
		return start + n
	}
	return len(content)
}

func grepLineContext(content []byte, start, end, count int) (before, after []string) {
	for cursor := start; cursor > 0 && len(before) < count; {
		previous := bytes.LastIndexByte(content[:cursor-1], '\n') + 1
		before = append(before, string(bytes.TrimSuffix(content[previous:cursor-1], []byte{'\r'})))
		cursor = previous
	}
	for left, right := 0, len(before)-1; left < right; left, right = left+1, right-1 {
		before[left], before[right] = before[right], before[left]
	}
	for cursor := end + 1; cursor < len(content) && len(after) < count; {
		next := grepLineEnd(content, cursor)
		after = append(after, string(bytes.TrimSuffix(content[cursor:next], []byte{'\r'})))
		cursor = next + 1
	}
	return before, after
}
