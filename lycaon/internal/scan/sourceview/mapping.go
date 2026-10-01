package sourceview

import "sort"

type Position struct {
	Line, Column int
}

type mappedSpan struct {
	generated, original, length int
}

// SourceMap translates one-based byte coordinates from a projection to its source.
type SourceMap struct {
	originalLines, generatedLines []int
	generatedLength               int
	spans                         []mappedSpan
}

func lineOffsets(source []byte) []int {
	lines := []int{0}
	for i, b := range source {
		if b == '\n' {
			lines = append(lines, i+1)
		}
	}
	return lines
}

func sourceOffset(position Position, lines []int, length int) (int, bool) {
	if position.Line < 1 || position.Line > len(lines) || position.Column < 1 {
		return 0, false
	}
	offset := lines[position.Line-1] + position.Column - 1
	end := length
	if position.Line < len(lines) {
		end = lines[position.Line] - 1
	}
	return offset, offset >= lines[position.Line-1] && offset <= end
}

func sourcePosition(offset int, lines []int) Position {
	line := sort.Search(len(lines), func(i int) bool { return lines[i] > offset })
	return Position{Line: line, Column: offset - lines[line-1] + 1}
}

func (m *SourceMap) MapPosition(position Position) (Position, bool) {
	offset, ok := sourceOffset(position, m.generatedLines, m.generatedLength)
	if !ok {
		return Position{}, false
	}
	offset, ok = m.originalOffset(offset, true)
	if !ok {
		return Position{}, false
	}
	return sourcePosition(offset, m.originalLines), true
}

func (m *SourceMap) MapSpan(start, end Position) (Position, Position, bool) {
	first, ok := sourceOffset(start, m.generatedLines, m.generatedLength)
	if !ok {
		return Position{}, Position{}, false
	}
	last, ok := sourceOffset(end, m.generatedLines, m.generatedLength)
	if !ok || last < first {
		return Position{}, Position{}, false
	}
	first, ok = m.originalOffset(first, false)
	if !ok {
		return Position{}, Position{}, false
	}
	last, ok = m.originalOffset(last, true)
	if !ok || last < first {
		return Position{}, Position{}, false
	}
	return sourcePosition(first, m.originalLines), sourcePosition(last, m.originalLines), true
}

func (m *SourceMap) originalOffset(offset int, end bool) (int, bool) {
	i := sort.Search(len(m.spans), func(i int) bool {
		boundary := m.spans[i].generated + m.spans[i].length
		return boundary > offset || end && boundary == offset
	})
	if i == len(m.spans) || offset < m.spans[i].generated {
		return 0, false
	}
	span := m.spans[i]
	return span.original + offset - span.generated, true
}

type scriptFragment struct {
	start, end int
}

type scriptBuilder struct {
	originalLines []int
	original      []byte
	extension     string
	fragments     []scriptFragment
}

func (b *scriptBuilder) build() ScriptProjection {
	var generated []byte
	var spans []mappedSpan
	for _, fragment := range b.fragments {
		if len(generated) > 0 {
			generated = append(generated, '\n', ';', '\n')
		}
		spans = append(spans, mappedSpan{generated: len(generated), original: fragment.start, length: fragment.end - fragment.start})
		generated = append(generated, b.original[fragment.start:fragment.end]...)
	}
	return ScriptProjection{Source: generated, Extension: b.extension, Map: &SourceMap{
		originalLines: b.originalLines, generatedLines: lineOffsets(generated),
		generatedLength: len(generated), spans: spans,
	}}
}
