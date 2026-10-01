package gotreesitter

import (
	"encoding/binary"
	"errors"
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

// InputEncoding identifies the source encoding used to build a Tree.
type InputEncoding uint8

const (
	InputEncodingUTF8 InputEncoding = iota
	InputEncodingUTF16
)

func (e InputEncoding) String() string {
	switch e {
	case InputEncodingUTF8:
		return "utf8"
	case InputEncodingUTF16:
		return "utf16"
	default:
		return "unknown"
	}
}

// UTF16ByteOrder identifies the byte order used by a UTF-16 byte source.
type UTF16ByteOrder uint8

const (
	UTF16LittleEndian UTF16ByteOrder = iota
	UTF16BigEndian
)

func (o UTF16ByteOrder) String() string {
	switch o {
	case UTF16LittleEndian:
		return "utf16le"
	case UTF16BigEndian:
		return "utf16be"
	default:
		return "unknown"
	}
}

var (
	// ErrInvalidUTF16ByteLength is returned when a UTF-16 byte source has a
	// dangling trailing byte.
	ErrInvalidUTF16ByteLength = errors.New("utf16: byte source length must be even")

	// ErrInvalidUTF16ByteOrder is returned for an unknown UTF-16ByteOrder.
	ErrInvalidUTF16ByteOrder = errors.New("utf16: invalid byte order")

	// ErrInvalidUTF16Range is returned when a UTF-16 range does not align to
	// valid code-point boundaries or has an inverted span.
	ErrInvalidUTF16Range = errors.New("utf16: invalid range")
)

// DecodeUTF16Bytes decodes an endian-specific UTF-16 byte source into Go
// UTF-16 code units.
func DecodeUTF16Bytes(source []byte, order UTF16ByteOrder) ([]uint16, error) {
	if len(source)%2 != 0 {
		return nil, ErrInvalidUTF16ByteLength
	}

	var byteOrder binary.ByteOrder
	switch order {
	case UTF16LittleEndian:
		byteOrder = binary.LittleEndian
	case UTF16BigEndian:
		byteOrder = binary.BigEndian
	default:
		return nil, ErrInvalidUTF16ByteOrder
	}

	out := make([]uint16, len(source)/2)
	for i := range out {
		out[i] = byteOrder.Uint16(source[i*2:])
	}
	return out, nil
}

// IncludedRangesForUTF16 converts UTF-16 included ranges into the parser's
// internal UTF-8 byte ranges. The returned Range points use UTF-8 columns.
func IncludedRangesForUTF16(source []uint16, ranges []UTF16Range) ([]Range, bool) {
	_, sourceMap := encodeUTF16ToUTF8WithMap(source)
	out, ok := sourceMap.includedRangesForUTF16(ranges)
	if !ok {
		return nil, false
	}
	return out, true
}

// IncludedRangesForUTF16Bytes converts endian-specific UTF-16 byte ranges into
// the parser's internal UTF-8 byte ranges. The returned Range points use UTF-8
// columns.
func IncludedRangesForUTF16Bytes(source []byte, order UTF16ByteOrder, ranges []UTF16Range) ([]Range, error) {
	units, err := DecodeUTF16Bytes(source, order)
	if err != nil {
		return nil, err
	}
	out, ok := IncludedRangesForUTF16(units, ranges)
	if !ok {
		return nil, ErrInvalidUTF16Range
	}
	return out, nil
}

// UTF16Range is a source range in UTF-16 code units.
//
// StartPoint and EndPoint use UTF-16 code-unit columns, matching the coordinate
// system used by many editors and LSP clients.
type UTF16Range struct {
	StartCodeUnit uint32
	EndCodeUnit   uint32
	StartPoint    Point
	EndPoint      Point
}

// UTF16Edit describes a source edit in UTF-16 code-unit offsets.
type UTF16Edit struct {
	StartCodeUnit  uint32
	OldEndCodeUnit uint32
	NewEndCodeUnit uint32
}

type utf16SourceMap struct {
	source []uint16

	// utf8 contains the canonical UTF-8 source used by the parser core.
	utf8 []byte

	// byteToUnit maps UTF-8 byte offsets to UTF-16 code-unit offsets.
	byteToUnit   []uint32
	byteBoundary []bool

	// unitToByte maps UTF-16 code-unit offsets to UTF-8 byte offsets.
	unitToByte   []uint32
	unitBoundary []bool

	// lineStartUnits stores the UTF-16 code-unit offset at each line start.
	lineStartUnits []uint32

	// lineStartBytes stores the UTF-8 byte offset at each line start.
	lineStartBytes []uint32
}

func encodeUTF16ToUTF8WithMap(source []uint16) ([]byte, *utf16SourceMap) {
	lineStartCap := min(len(source)+1, 128)
	m := &utf16SourceMap{
		source:         source,
		unitToByte:     make([]uint32, len(source)+1),
		unitBoundary:   make([]bool, len(source)+1),
		byteToUnit:     make([]uint32, 1, len(source)+1),
		byteBoundary:   make([]bool, 1, len(source)+1),
		lineStartUnits: make([]uint32, 1, lineStartCap),
		lineStartBytes: make([]uint32, 1, lineStartCap),
	}
	m.byteBoundary[0] = true
	m.unitBoundary[0] = true
	if len(source) == 0 {
		return nil, m
	}

	// ASCII source is common for editor buffers even when represented as UTF-16.
	// Start with len(source) and let append grow only when non-ASCII expands.
	out := make([]byte, 0, len(source))
	for unitPos := 0; unitPos < len(source); {
		unitStart := unitPos
		byteStart := len(out)
		r, unitSize := decodeUTF16Rune(source, unitPos)
		unitPos += unitSize

		var encoded [utf8.UTFMax]byte
		byteSize := utf8.EncodeRune(encoded[:], r)
		out = append(out, encoded[:byteSize]...)

		unitEnd := unitStart + unitSize
		byteEnd := byteStart + byteSize
		m.unitToByte[unitStart] = uint32(byteStart)
		for u := unitStart + 1; u < unitEnd; u++ {
			m.unitToByte[u] = uint32(byteStart)
		}
		m.unitToByte[unitEnd] = uint32(byteEnd)
		m.unitBoundary[unitStart] = true
		m.unitBoundary[unitEnd] = true

		for i := 1; i <= byteSize; i++ {
			unit := uint32(unitStart)
			if i == byteSize {
				unit = uint32(unitEnd)
			}
			m.byteToUnit = append(m.byteToUnit, unit)
			m.byteBoundary = append(m.byteBoundary, i == byteSize)
		}
		if r == '\n' {
			m.lineStartUnits = append(m.lineStartUnits, uint32(unitEnd))
			m.lineStartBytes = append(m.lineStartBytes, uint32(byteEnd))
		}
	}
	m.utf8 = out
	return out, m
}

func decodeUTF16Rune(source []uint16, pos int) (rune, int) {
	r := rune(source[pos])
	if utf16.IsSurrogate(r) {
		if pos+1 < len(source) {
			r2 := rune(source[pos+1])
			decoded := utf16.DecodeRune(r, r2)
			if decoded != unicodeReplacementRune {
				return decoded, 2
			}
		}
		return unicodeReplacementRune, 1
	}
	return r, 1
}

const unicodeReplacementRune = '\uFFFD'

func (m *utf16SourceMap) byteToUTF16Unit(offset uint32) (uint32, bool) {
	if m == nil || offset > uint32(len(m.byteToUnit)-1) {
		return 0, false
	}
	if int(offset) >= len(m.byteBoundary) || !m.byteBoundary[offset] {
		return 0, false
	}
	return m.byteToUnit[offset], true
}

func (m *utf16SourceMap) utf16UnitToByte(offset uint32) (uint32, bool) {
	if m == nil || offset > uint32(len(m.unitToByte)-1) {
		return 0, false
	}
	if int(offset) >= len(m.unitBoundary) || !m.unitBoundary[offset] {
		return 0, false
	}
	return m.unitToByte[offset], true
}

func (m *utf16SourceMap) pointForUnit(offset uint32) (Point, bool) {
	if m == nil || offset > uint32(len(m.source)) {
		return Point{}, false
	}
	row := sort.Search(len(m.lineStartUnits), func(i int) bool {
		return m.lineStartUnits[i] > offset
	}) - 1
	if row < 0 {
		row = 0
	}
	lineStart := m.lineStartUnits[row]
	return Point{Row: uint32(row), Column: offset - lineStart}, true
}

func (m *utf16SourceMap) pointForByte(offset uint32) (Point, bool) {
	unit, ok := m.byteToUTF16Unit(offset)
	if !ok {
		return Point{}, false
	}
	return m.pointForUnit(unit)
}

func (m *utf16SourceMap) pointForUTF8Byte(offset uint32) (Point, bool) {
	if m == nil || offset > uint32(len(m.utf8)) {
		return Point{}, false
	}
	row := sort.Search(len(m.lineStartBytes), func(i int) bool {
		return m.lineStartBytes[i] > offset
	}) - 1
	if row < 0 {
		row = 0
	}
	lineStart := m.lineStartBytes[row]
	return Point{Row: uint32(row), Column: offset - lineStart}, true
}

func (m *utf16SourceMap) rangeForNode(n *Node) (UTF16Range, bool) {
	if m == nil || n == nil {
		return UTF16Range{}, false
	}
	return m.rangeForByteRange(n.StartByte(), n.EndByte())
}

func (m *utf16SourceMap) rangeForByteRange(startByte, endByte uint32) (UTF16Range, bool) {
	if m == nil || endByte < startByte {
		return UTF16Range{}, false
	}
	startUnit, ok := m.byteToUTF16Unit(startByte)
	if !ok {
		return UTF16Range{}, false
	}
	endUnit, ok := m.byteToUTF16Unit(endByte)
	if !ok {
		return UTF16Range{}, false
	}
	startPoint, ok := m.pointForUnit(startUnit)
	if !ok {
		return UTF16Range{}, false
	}
	endPoint, ok := m.pointForUnit(endUnit)
	if !ok {
		return UTF16Range{}, false
	}
	return UTF16Range{
		StartCodeUnit: startUnit,
		EndCodeUnit:   endUnit,
		StartPoint:    startPoint,
		EndPoint:      endPoint,
	}, true
}

func (m *utf16SourceMap) includedRangesForUTF16(ranges []UTF16Range) ([]Range, bool) {
	if len(ranges) == 0 {
		return nil, true
	}
	if m == nil {
		return nil, false
	}
	out := make([]Range, 0, len(ranges))
	for _, r := range ranges {
		if r.EndCodeUnit < r.StartCodeUnit {
			return nil, false
		}
		startByte, ok := m.utf16UnitToByte(r.StartCodeUnit)
		if !ok {
			return nil, false
		}
		endByte, ok := m.utf16UnitToByte(r.EndCodeUnit)
		if !ok {
			return nil, false
		}
		startPoint, ok := m.pointForUTF8Byte(startByte)
		if !ok {
			return nil, false
		}
		endPoint, ok := m.pointForUTF8Byte(endByte)
		if !ok {
			return nil, false
		}
		out = append(out, Range{
			StartByte:  startByte,
			EndByte:    endByte,
			StartPoint: startPoint,
			EndPoint:   endPoint,
		})
	}
	return normalizeIncludedRanges(out), true
}

func utf16Boundary(source []uint16, offset uint32) bool {
	if offset > uint32(len(source)) {
		return false
	}
	if offset == 0 || offset == uint32(len(source)) {
		return true
	}
	prev := source[offset-1]
	next := source[offset]
	return !isUTF16LeadSurrogate(prev) || !isUTF16TrailSurrogate(next)
}

func measureUTF16AsUTF8(source []uint16) (uint32, Point) {
	var byteLen uint32
	point := Point{}
	for unitPos := 0; unitPos < len(source); {
		r, unitSize := decodeUTF16Rune(source, unitPos)
		unitPos += unitSize
		byteSize := uint32(utf8.RuneLen(r))
		byteLen += byteSize
		if r == '\n' {
			point.Row++
			point.Column = 0
			continue
		}
		point.Column += byteSize
	}
	return byteLen, point
}

func addPointDelta(start, delta Point) Point {
	if delta.Row == 0 {
		return Point{Row: start.Row, Column: start.Column + delta.Column}
	}
	return Point{Row: start.Row + delta.Row, Column: delta.Column}
}

func isUTF16LeadSurrogate(u uint16) bool {
	return 0xD800 <= u && u <= 0xDBFF
}

func isUTF16TrailSurrogate(u uint16) bool {
	return 0xDC00 <= u && u <= 0xDFFF
}
