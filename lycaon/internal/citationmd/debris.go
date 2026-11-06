package citationmd

import "strings"

// maxLineMarkupBytes bounds the list and quote markers that may lead a line.
const maxLineMarkupBytes = 32

// separatorLength measures a separator at the start of text: punctuation or a
// conjunction whose only job was to join references.
func separatorLength(text string) int {
	if text == "" {
		return 0
	}
	if text[0] == ',' || text[0] == ';' {
		return 1
	}
	for _, word := range []string{"and ", "or ", "& "} {
		if strings.HasPrefix(text, word) {
			return len(word) - 1
		}
	}
	return 0
}

// joinsRemoved reports text between two removed nodes that says nothing without
// them: whitespace on one line, or one separator with its whitespace.
func joinsRemoved(gap string) bool {
	if isSpaces(gap) {
		return true
	}
	if strings.IndexByte(gap, '\n') >= 0 {
		return false
	}
	trimmed := strings.TrimLeft(gap, " \t")
	length := separatorLength(trimmed + " ")
	return length > 0 && isSpaces(trimmed[min(length, len(trimmed)):])
}

// tidyAround settles the source bytes that touch a removed run, and only those: an
// emptied bracket pair goes, and a separator left with nothing on one side goes. It
// returns where text resumes after the run, no further than limit.
func (w *renderWalk) tidyAround(runEnd, limit int) int {
	ended := limit == w.doc.Body.End
	if newline := strings.IndexByte(w.doc.Source[runEnd:limit], '\n'); newline >= 0 {
		limit, ended = runEnd+newline, true
	}
	for {
		rest := w.doc.Source[runEnd:limit]
		space := len(rest) - len(strings.TrimLeft(rest, " \t\r"))
		right := rest[space:]
		left := w.out.leftNeighbour()
		opened := closerOf(left) != 0
		separator := separatorLength(right)
		closes := (right == "" && ended) || (right != "" && closesAfterSeparator(right[0]))
		switch {
		case opened && right != "" && closerOf(left) == right[0]:
			w.out.retractNeighbour()
			runEnd += space + 1
		case separator > 0 && (opened || w.startsLine()):
			runEnd = pastSpace(w.doc.Source, runEnd+space+separator, limit)
		case opened && right != "":
			w.out.trimPending()
			return runEnd + space
		case isSeparatorByte(left) && separator > 0:
			return pastSpace(w.doc.Source, runEnd+space+separator, limit)
		case isSeparatorByte(left) && closes && !w.out.insideLinkDestination():
			w.out.retractNeighbour()
		case closes && w.out.retractConjunction():
		default:
			return runEnd
		}
	}
}

// startsLine reports that only markup precedes the run on its line. The length
// check keeps this constant-time on a long line of many references.
func (w *renderWalk) startsLine() bool {
	return w.runStart-w.lineHead <= maxLineMarkupBytes && w.prefixEmptied()
}

func pastSpace(source string, at, limit int) int {
	for at < limit && isSpaceByte(source[at]) {
		at++
	}
	return at
}

// closerOf covers parentheses only: closing up square brackets could make a link.
func closerOf(opener byte) byte {
	if opener == '(' {
		return ')'
	}
	return 0
}

func isSeparatorByte(b byte) bool { return b == ',' || b == ';' }

func closesAfterSeparator(b byte) bool { return strings.IndexByte(`.;:!?)]`, b) >= 0 }
