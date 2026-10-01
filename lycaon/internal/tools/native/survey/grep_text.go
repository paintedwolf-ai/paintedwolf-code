package survey

import "bytes"

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

// Only matching lines allocate surrounding context.
func (s *grepSearch) forEachTextMatch(rel string, content []byte, yield func(grepMatch) bool) error {
	lineNo := 0
	for start := 0; start < len(content); {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		lineNo++
		end := grepLineEnd(content, start)
		line := string(bytes.TrimSuffix(content[start:end], []byte{'\r'}))
		matched, matchText := lineMatches(line, s.re)
		if matched {
			entry := grepMatch{Path: rel, Line: lineNo, Content: line, Match: matchText}
			if s.contextLines > 0 {
				entry.ContextBefore, entry.ContextAfter = grepLineContext(content, start, end, s.contextLines)
			}
			if !yield(entry) {
				return nil
			}
		}
		start = end + 1
	}
	return nil
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
