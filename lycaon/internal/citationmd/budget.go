package citationmd

import "regexp"

const (
	// maxStructureWork bounds the bytes the structural parser may rescan for the
	// shapes it handles quadratically. Ordinary documents stay orders below it.
	maxStructureWork = 1 << 27
	// maxContainerDepth bounds nested block containers on one line.
	maxContainerDepth = 64
	// maxTableCells bounds a table's columns times rows. The parser pads every
	// short row to the header width, so a small source can allocate this many.
	maxTableCells = 1 << 16
	// maxBracketDepth bounds unclosed brackets in one block.
	maxBracketDepth = 256
	// maxDeepItemIndent bounds the summed indent of deeply indented list items,
	// the other way to nest containers without repeating markers on a line.
	maxDeepItemIndent = 1 << 17
	deepItemIndent    = 32
)

// rawOpeners are the inline raw HTML forms the parser rescans to the end of the
// block when they never close.
var rawOpeners = []struct{ opener, closer string }{
	{"<?", "?>"}, {"<!--", "-->"}, {"<![CDATA[", "]]>"},
}

var (
	tableDelimiterRow = regexp.MustCompile(`^[ \t>]*\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*\r?\n?$`)
	definitionLine    = regexp.MustCompile(`^[ \t>]*\[[^\]\n]*\]:`)
)

// structureBudget tallies one pass over a source, block by block. Its counters
// are the facts that make the structural parse expensive.
type structureBudget struct {
	work       int
	cells      int
	columns    int
	itemIndent int
	depth      int
	maxDepth   int
	deepLine   bool
	blockBytes int
	defs       int
	unclosed   [3]int
}

// structureExceeded reports whether parsing source would exceed a declared
// structural budget. It runs in time linear in the source and allocates nothing
// that grows with it.
func structureExceeded(source string) bool {
	var budget structureBudget
	for at := 0; at < len(source); {
		end := lineEnd(source, at)
		budget.line(source[at:end])
		if budget.over() {
			return true
		}
		at = end
	}
	budget.endBlock()
	return budget.over()
}

func (b *structureBudget) over() bool {
	return b.deepLine || b.work > maxStructureWork || b.cells > maxTableCells ||
		b.maxDepth > maxBracketDepth || b.itemIndent > maxDeepItemIndent
}

func (b *structureBudget) line(line string) {
	if isBlankLine(line) {
		b.endBlock()
		return
	}
	b.blockBytes += len(line)
	b.deepLine = b.deepLine || lineContainerDepth(line) > maxContainerDepth
	if indent, ok := itemMarkerIndent(line); ok && indent >= deepItemIndent {
		b.itemIndent += indent
	}
	if definitionLine.MatchString(line) {
		b.defs++
	}
	if tableDelimiterRow.MatchString(line) {
		if columns := countByte(line, '|') + 1; columns > b.columns {
			b.columns = columns
		}
	}
	b.cells += b.columns
	b.scanInline(line)
}

// scanInline counts bracket depth and the raw HTML openers left without a closer.
func (b *structureBudget) scanInline(line string) {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '[':
			if b.depth++; b.depth > b.maxDepth {
				b.maxDepth = b.depth
			}
		case ']':
			if b.depth > 0 {
				b.depth--
			}
		case '<', '-', '?':
			b.scanRawForm(line[i:])
		}
	}
}

func (b *structureBudget) scanRawForm(rest string) {
	for kind, form := range rawOpeners {
		if hasPrefix(rest, form.opener) {
			b.unclosed[kind]++
		} else if hasPrefix(rest, form.closer) {
			b.unclosed[kind] = 0
		}
	}
}

func (b *structureBudget) endBlock() {
	b.work += b.defs * b.blockBytes
	for _, count := range b.unclosed {
		b.work += count * b.blockBytes
	}
	b.defs, b.blockBytes, b.columns, b.depth = 0, 0, 0, 0
	b.unclosed = [3]int{}
}

func hasPrefix(text, prefix string) bool {
	return len(text) >= len(prefix) && text[:len(prefix)] == prefix
}

func countByte(text string, c byte) int {
	count := 0
	for i := 0; i < len(text); i++ {
		if text[i] == c {
			count++
		}
	}
	return count
}

func isBlankLine(line string) bool {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}

// lineContainerDepth counts container markers opening a line. A thematic break
// looks like repeated markers and opens none.
func lineContainerDepth(line string) int {
	if isThematicBreak(line) {
		return 0
	}
	depth, at := 0, 0
	for at < len(line) {
		width := containerMarkerWidth(line[at:])
		if width == 0 {
			break
		}
		at += width
		depth++
	}
	return depth
}

func isThematicBreak(line string) bool {
	marker, count := byte(0), 0
	for i := 0; i < len(line); i++ {
		switch c := line[i]; c {
		case ' ', '\t', '\r', '\n':
		case '-', '*', '_':
			if marker != 0 && c != marker {
				return false
			}
			marker = c
			count++
		default:
			return false
		}
	}
	return count >= 3
}

// itemMarkerIndent reports the indent before a line's first list marker.
func itemMarkerIndent(line string) (int, bool) {
	indent := 0
	for indent < len(line) && isSpaceByte(line[indent]) {
		indent++
	}
	if indent == len(line) || line[indent] == '>' {
		return 0, false
	}
	return indent, containerMarkerWidth(line[indent:]) > 0
}

// containerMarkerWidth measures one container marker and the whitespace before
// it at the start of rest, or zero when none starts there.
func containerMarkerWidth(rest string) int {
	at := 0
	for at < len(rest) && isSpaceByte(rest[at]) {
		at++
	}
	if at == len(rest) {
		return 0
	}
	switch c := rest[at]; {
	case c == '>':
		return at + 1
	case c == '-' || c == '+' || c == '*':
		if at+1 < len(rest) && isSpaceByte(rest[at+1]) {
			return at + 2
		}
	case c >= '0' && c <= '9':
		digits := at
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' && digits-at < 9 {
			digits++
		}
		if digits+1 < len(rest) && (rest[digits] == '.' || rest[digits] == ')') && isSpaceByte(rest[digits+1]) {
			return digits + 2
		}
	}
	return 0
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' }

// coarseLiteralZones finds fences and same-line code spans without a structural
// parse, for a source past the budgets. It errs toward literal, so a fallback
// removes nothing from a fence-like region.
func coarseLiteralZones(source string) []Zone {
	var zones []Zone
	var fence byte
	run, open := 0, 0
	for at := 0; at < len(source); {
		end := lineEnd(source, at)
		char, length, rest := fenceRun(source[at:end])
		switch {
		case fence == 0 && length >= 3:
			fence, run, open = char, length, at
		case fence != 0 && char == fence && length >= run && isBlankLine(rest):
			zones = append(zones, Zone{Kind: ZoneFence, Span: Span{open, end}})
			fence = 0
		case fence == 0:
			zones = append(zones, coarseCodeSpans(source, at, end)...)
		}
		at = end
	}
	if fence != 0 {
		zones = append(zones, Zone{Kind: ZoneFence, Span: Span{open, len(source)}})
	}
	return zones
}

// fenceRun skips container markup and reads a fence run at the start of a line.
func fenceRun(line string) (char byte, length int, rest string) {
	at := 0
	for at < len(line) && (isSpaceByte(line[at]) || line[at] == '>') {
		at++
	}
	if at == len(line) || (line[at] != '`' && line[at] != '~') {
		return 0, 0, line
	}
	char = line[at]
	end := at
	for end < len(line) && line[end] == char {
		end++
	}
	return char, end - at, line[end:]
}

func coarseCodeSpans(source string, at, end int) []Zone {
	var zones []Zone
	for i := at; i < end; i++ {
		if source[i] != '`' {
			continue
		}
		open := i
		for i < end && source[i] == '`' {
			i++
		}
		length := i - open
		for j := i; j < end; j++ {
			if source[j] != '`' {
				continue
			}
			close := j
			for j < end && source[j] == '`' {
				j++
			}
			if j-close == length {
				zones = append(zones, Zone{Kind: ZoneCodeSpan, Span: Span{open, j}})
				i = j - 1
				break
			}
		}
	}
	return zones
}
