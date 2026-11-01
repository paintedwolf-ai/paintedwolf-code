package detect

import (
	"github.com/zricethezav/gitleaks/v8/detect/budget"
	"strings"
)

// Location represents a location in a file
type Location struct {
	startLine      int
	endLine        int
	startColumn    int
	endColumn      int
	startLineIndex int
	endLineIndex   int
}

func location(newlineIndices [][]int, raw string, matchIndex []int) Location {
	if len(newlineIndices) == 0 {
		newlineIndices = [][]int{{len(raw), len(raw) + 1}}
	}
	at := 0
	return locationNext(raw, matchIndex, func() (int, bool) {
		if at == len(newlineIndices) {
			return 0, false
		}
		index := newlineIndices[at][0]
		at++
		return index, true
	})
}

// locationRaw feeds the unchanged location calculation directly from original
// bytes, avoiding a whole-file newline index per bounded rule invocation.
func locationRaw(raw string, matchIndex []int, meter *budget.Meter) Location {
	at := 0
	empty := !strings.Contains(raw, "\n")
	return locationNext(raw, matchIndex, func() (int, bool) {
		meter.Check()
		if empty {
			empty = false
			at = len(raw)
			return len(raw), true
		}
		next := strings.IndexByte(raw[at:], '\n')
		if next < 0 {
			return 0, false
		}
		next += at
		at = next + 1
		return next, true
	})
}

func locationNext(raw string, matchIndex []int, next func() (int, bool)) Location {

	var (
		prevNewLine int
		location    Location
		lineSet     bool
		_lineNum    int
	)

	start := matchIndex[0]
	end := matchIndex[1]

	// default startLineIndex to 0
	location.startLineIndex = 0

	for lineNum := 0; ; lineNum++ {
		newLineByteIndex, ok := next()
		if !ok {
			break
		}
		_lineNum = lineNum
		if prevNewLine <= start && start < newLineByteIndex {
			lineSet = true
			location.startLine = lineNum
			location.endLine = lineNum
			location.startColumn = (start - prevNewLine) + 1 // +1 because counting starts at 1
			location.startLineIndex = prevNewLine
			location.endLineIndex = newLineByteIndex
		}
		if prevNewLine < end && end <= newLineByteIndex {
			location.endLine = lineNum
			location.endColumn = (end - prevNewLine)
			location.endLineIndex = newLineByteIndex
		}

		prevNewLine = newLineByteIndex
	}

	if !lineSet {
		// if lines never get set then that means the secret is most likely
		// on the last line of the diff output and the diff output does not have
		// a newline
		location.startColumn = (start - prevNewLine) + 1 // +1 because counting starts at 1
		location.endColumn = (end - prevNewLine)
		location.startLine = _lineNum + 1
		location.endLine = _lineNum + 1

		// search for new line byte index
		i := 0
		for end+i < len(raw) {
			if raw[end+i] == '\n' {
				break
			}
			if raw[end+i] == '\r' {
				break
			}
			i++
		}
		location.endLineIndex = end + i
	}
	return location
}
