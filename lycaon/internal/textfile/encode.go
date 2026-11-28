package textfile

import (
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// EncodedSize validates text and measures its exact selected representation
// without allocating the representation. Callers can admit capacity first.
func EncodedSize(content, encoding string) (int64, error) {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return 0, ErrBinary
	}
	size, ok := encodedSize(content, encoding)
	if !ok {
		return 0, ErrUnsupported
	}
	return size, nil
}

func encodedSize(content, encoding string) (int64, bool) {
	switch encoding {
	case UTF8:
		return int64(len(content)), true
	case UTF8BOM:
		return int64(len(content)) + 3, true
	case UTF16LE, UTF16BE, UTF16LEBOM, UTF16BEBOM:
		var size int64
		if encoding == UTF16LEBOM || encoding == UTF16BEBOM {
			size = 2
		}
		for _, r := range content {
			if r > 0xffff {
				size += 4
			} else {
				size += 2
			}
		}
		return size, true
	default:
		return 0, false
	}
}

// EncodeBounded rejects oversized output before allocating encoded bytes.
func EncodeBounded(content, encoding string, limits Limits) ([]byte, error) {
	if limits.MaxRawBytes <= 0 || limits.MaxTextBytes <= 0 {
		return nil, ErrLimitsInvalid
	}
	size, err := EncodedSize(content, encoding)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limits.MaxTextBytes {
		return nil, ErrTextTooLarge
	}
	if size > limits.MaxRawBytes {
		return nil, ErrRawTooLarge
	}
	encoded, _ := encodeUnchecked(content, encoding)
	return encoded, nil
}

// encodeUnchecked also creates deliberately invalid fixtures for the codec's
// classifier tests; production calls validate through EncodeBounded first.
func encodeUnchecked(content, encoding string) ([]byte, bool) {
	switch encoding {
	case UTF8:
		return []byte(content), true
	case UTF8BOM:
		out := make([]byte, len(bomUTF8)+len(content))
		copy(out, bomUTF8)
		copy(out[len(bomUTF8):], content)
		return out, true
	case UTF16LE:
		return encodeUTF16(content, false, false), true
	case UTF16LEBOM:
		return encodeUTF16(content, false, true), true
	case UTF16BE:
		return encodeUTF16(content, true, false), true
	case UTF16BEBOM:
		return encodeUTF16(content, true, true), true
	default:
		return nil, false
	}
}

// encodeUTF16 writes directly into its exact byte capacity. It retains no
// intermediate rune or code-unit array.
func encodeUTF16(content string, bigEndian, bom bool) []byte {
	encoding := UTF16LE
	if bom {
		encoding = UTF16LEBOM
	}
	size, _ := encodedSize(content, encoding)
	out := make([]byte, 0, int(size))
	if bom {
		if bigEndian {
			out = append(out, bomUTF16BE...)
		} else {
			out = append(out, bomUTF16LE...)
		}
	}
	unit := func(value uint16) {
		if bigEndian {
			out = append(out, byte(value>>8), byte(value&0xff))
		} else {
			out = append(out, byte(value&0xff), byte(value>>8))
		}
	}
	for _, r := range content {
		if r <= 0xffff {
			unit(uint16(r & 0xffff))
		} else {
			first, second := utf16.EncodeRune(r)
			unit(uint16(first & 0xffff))
			unit(uint16(second & 0xffff))
		}
	}
	return out
}

// SerializeEOL writes LF-normalized text with its recorded line ending, "lf"
// or "crlf".
func SerializeEOL(content, eol string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if eol == "crlf" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

// HasMixedEOL reports whether content contains both CRLF and bare LF line endings.
func HasMixedEOL(content string) bool {
	crlf := strings.Count(content, "\r\n")
	lf := strings.Count(content, "\n") - crlf
	return crlf > 0 && lf > 0
}

type rawLine struct {
	body string
	eol  string
}

func splitRawLines(s string) []rawLine {
	if s == "" {
		return nil
	}
	var out []rawLine
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			out = append(out, rawLine{body: s, eol: ""})
			break
		}
		if idx > 0 && s[idx-1] == '\r' {
			out = append(out, rawLine{body: s[:idx-1], eol: "\r\n"})
		} else {
			out = append(out, rawLine{body: s[:idx], eol: "\n"})
		}
		s = s[idx+1:]
	}
	return out
}

// maxMixedEOLLCSCells bounds interior LCS dynamic programming work so large
// all-rewritten interiors stay strictly linear in line count.
const maxMixedEOLLCSCells = 65536

// maxMixedEOLMyersD bounds the shortest-edit-script distance explored by
// alignGapMyers. Any gap within this edit distance (even across thousands of
// identical repeated interior lines with no unique anchors) aligns exactly in
// O((m+n)*D) time, while large all-rewritten gaps stay bounded in O(maxD^2).
const maxMixedEOLMyersD = 128

// ErrMixedEOLAlignmentUnresolved is returned when a mixed-EOL interior gap
// exceeds both bounded alignment caps while surviving original line bodies
// remain, so untouched line terminators cannot be preserved safely.
var ErrMixedEOLAlignmentUnresolved = errors.New("mixed-eol interior alignment exceeded bounded budget with surviving lines")

// SerializePreservingMixedEOL serializes LF-normalized draft text while keeping
// the exact original per-line terminators ("\r\n" vs "\n") of untouched lines
// from beforeRawText. Modified or newly inserted lines take fallbackEOL.
// If an interior gap exceeds both bounded alignment caps while surviving
// original lines remain, it fails closed with ErrMixedEOLAlignmentUnresolved.
func SerializePreservingMixedEOL(beforeRawText, draft, fallbackEOL string) (string, bool, error) {
	serialized, mixed, _, err := serializePreservingMixedEOLWithStats(beforeRawText, draft, fallbackEOL)
	return serialized, mixed, err
}

func serializePreservingMixedEOLWithStats(beforeRawText, draft, fallbackEOL string) (string, bool, int, error) {
	if beforeRawText == "" || !HasMixedEOL(beforeRawText) {
		serialized := SerializeEOL(draft, fallbackEOL)
		return serialized, false, 0, nil
	}
	beforeLines := splitRawLines(beforeRawText)
	draftLines := splitRawLines(strings.ReplaceAll(draft, "\r\n", "\n"))
	if len(draftLines) == 0 {
		return "", false, 0, nil
	}
	fallbackTerm := "\n"
	if fallbackEOL == "crlf" {
		fallbackTerm = "\r\n"
	}

	steps := 0
	// Match common unchanged prefix and suffix lines by exact body.
	prefix := 0
	for prefix < len(beforeLines) && prefix < len(draftLines) && beforeLines[prefix].body == draftLines[prefix].body {
		prefix++
		steps++
	}
	beforeEnd := len(beforeLines)
	draftEnd := len(draftLines)
	for beforeEnd > prefix && draftEnd > prefix && beforeLines[beforeEnd-1].body == draftLines[draftEnd-1].body {
		beforeEnd--
		draftEnd--
		steps++
	}

	// Align interior [prefix:draftEnd) against [prefix:beforeEnd) using bounded
	// patience anchoring (unique lines + LIS) with bounded Myers SES and LCS DP.
	midMatch := make([]int, draftEnd-prefix)
	for i := range midMatch {
		midMatch[i] = -1
	}
	if beforeEnd > prefix && draftEnd > prefix {
		alignSteps, err := alignInteriorBounded(beforeLines[prefix:beforeEnd], draftLines[prefix:draftEnd], prefix, midMatch)
		steps += alignSteps
		if err != nil {
			return "", false, steps, err
		}
	}

	var b strings.Builder
	b.Grow(len(draft) + len(draftLines))
	for j, dl := range draftLines {
		b.WriteString(dl.body)
		if dl.eol == "" {
			continue
		}
		origIdx := -1
		switch {
		case j < prefix:
			origIdx = j
		case j >= draftEnd:
			origIdx = beforeEnd + (j - draftEnd)
		default:
			origIdx = midMatch[j-prefix]
		}
		if origIdx >= 0 && origIdx < len(beforeLines) && beforeLines[origIdx].eol != "" {
			b.WriteString(beforeLines[origIdx].eol)
		} else {
			b.WriteString(fallbackTerm)
		}
	}
	serialized := b.String()
	return serialized, HasMixedEOL(serialized), steps, nil
}

// alignInteriorBounded maps unchanged interior draft lines to their original
// indices (offset by baseOffset) using O(N log N) patience anchors on unique
// lines plus bounded LCS DP inside small gaps. Large rewritten gaps skip DP so
// total work is strictly bounded.
func alignInteriorBounded(beforeMid, draftMid []rawLine, baseOffset int, out []int) (int, error) {
	steps := 0
	bCount := make(map[string]int, len(beforeMid))
	bPos := make(map[string]int, len(beforeMid))
	for i, l := range beforeMid {
		bCount[l.body]++
		bPos[l.body] = i
		steps++
	}
	dCount := make(map[string]int, len(draftMid))
	for _, l := range draftMid {
		dCount[l.body]++
		steps++
	}

	type anchor struct{ bIdx, dIdx int }
	var candidates []anchor
	for j, l := range draftMid {
		if dCount[l.body] == 1 && bCount[l.body] == 1 {
			candidates = append(candidates, anchor{bIdx: bPos[l.body], dIdx: j})
		}
		steps++
	}

	// Longest increasing subsequence on bIdx in O(U log U).
	var anchors []anchor
	if len(candidates) > 0 {
		tails := make([]int, 0, len(candidates))
		prev := make([]int, len(candidates))
		for i, c := range candidates {
			lo, hi := 0, len(tails)
			for lo < hi {
				mid := lo + (hi-lo)/2
				steps++
				if candidates[tails[mid]].bIdx < c.bIdx {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo > 0 {
				prev[i] = tails[lo-1]
			} else {
				prev[i] = -1
			}
			if lo == len(tails) {
				tails = append(tails, i)
			} else {
				tails[lo] = i
			}
		}
		anchors = make([]anchor, len(tails))
		for idx, k := tails[len(tails)-1], len(tails)-1; k >= 0; k-- {
			anchors[k] = candidates[idx]
			idx = prev[idx]
		}
	}

	// Process each gap between consecutive anchors, plus leading and trailing gaps.
	bLo, dLo := 0, 0
	for _, a := range anchors {
		out[a.dIdx] = baseOffset + a.bIdx
		gapSteps, err := alignGapBounded(beforeMid, draftMid, bLo, a.bIdx, dLo, a.dIdx, baseOffset, out)
		steps += gapSteps
		if err != nil {
			return steps, err
		}
		bLo = a.bIdx + 1
		dLo = a.dIdx + 1
	}
	gapSteps, err := alignGapBounded(beforeMid, draftMid, bLo, len(beforeMid), dLo, len(draftMid), baseOffset, out)
	steps += gapSteps
	return steps, err
}

func alignGapBounded(beforeMid, draftMid []rawLine, bLo, bHi, dLo, dHi, baseOffset int, out []int) (int, error) {
	steps := 0
	// Trim matching prefix and suffix inside the gap (handles unchanged repeated lines adjacent to anchors).
	for bLo < bHi && dLo < dHi && beforeMid[bLo].body == draftMid[dLo].body {
		out[dLo] = baseOffset + bLo
		bLo++
		dLo++
		steps++
	}
	for bHi > bLo && dHi > dLo && beforeMid[bHi-1].body == draftMid[dHi-1].body {
		bHi--
		dHi--
		out[dHi] = baseOffset + bHi
		steps++
	}
	m := bHi - bLo
	n := dHi - dLo
	if m <= 0 || n <= 0 {
		return steps, nil
	}
	// First try bounded Myers O(ND) shortest-edit-script alignment so large
	// gaps with repeated bodies and small edit distance preserve every
	// unchanged line's exact terminator.
	aligned, myersSteps := alignGapMyers(beforeMid, draftMid, bLo, bHi, dLo, dHi, baseOffset, out)
	steps += myersSteps
	if aligned {
		return steps, nil
	}
	// Strictly bound fallback DP work: if m*n exceeds maxMixedEOLLCSCells,
	// allow fallback ONLY when no original line bodies survive in the gap
	// (e.g. a full interior rewrite). Otherwise fail closed so untouched
	// mixed-EOL lines are never silently normalized.
	if int64(m)*int64(n) > maxMixedEOLLCSCells {
		beforeBodies := make(map[string]struct{}, m)
		for i := bLo; i < bHi; i++ {
			beforeBodies[beforeMid[i].body] = struct{}{}
			steps++
		}
		for j := dLo; j < dHi; j++ {
			steps++
			if _, ok := beforeBodies[draftMid[j].body]; ok {
				return steps, ErrMixedEOLAlignmentUnresolved
			}
		}
		return steps, nil
	}
	cols := n + 1
	dp := make([]int, (m+1)*cols)
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			steps++
			if beforeMid[bLo+i].body == draftMid[dLo+j].body {
				dp[i*cols+j] = dp[(i+1)*cols+(j+1)] + 1
			} else if dp[(i+1)*cols+j] >= dp[i*cols+(j+1)] {
				dp[i*cols+j] = dp[(i+1)*cols+j]
			} else {
				dp[i*cols+j] = dp[i*cols+(j+1)]
			}
		}
	}
	i, j := 0, 0
	for i < m && j < n {
		steps++
		if beforeMid[bLo+i].body == draftMid[dLo+j].body {
			out[dLo+j] = baseOffset + bLo + i
			i++
			j++
		} else if dp[(i+1)*cols+j] >= dp[i*cols+(j+1)] {
			i++
		} else {
			j++
		}
	}
	return steps, nil
}

// alignGapMyers computes an exact shortest-edit-script alignment for
// beforeMid[bLo:bHi] vs draftMid[dLo:dHi] when the edit distance is at most
// maxMixedEOLMyersD. It returns false without modifying out if the edit
// distance exceeds the cap.
func alignGapMyers(beforeMid, draftMid []rawLine, bLo, bHi, dLo, dHi, baseOffset int, out []int) (bool, int) {
	m := bHi - bLo
	n := dHi - dLo
	maxD := maxMixedEOLMyersD
	if m+n < maxD {
		maxD = m + n
	}
	offset := maxD
	v := make([]int, 2*maxD+1)
	trace := make([][]int, 0, maxD+1)
	steps := 0
	foundD := -1

	for d := 0; d <= maxD; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)

		for k := -d; k <= d; k += 2 {
			steps++
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < m && y < n && beforeMid[bLo+x].body == draftMid[dLo+y].body {
				x++
				y++
				steps++
			}
			v[offset+k] = x
			if x == m && y == n {
				foundD = d
				break
			}
		}
		if foundD >= 0 {
			break
		}
	}
	if foundD < 0 {
		return false, steps
	}

	x, y := m, n
	for d := foundD; d > 0; d-- {
		prev := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && prev[offset+k-1] < prev[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prev[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			out[dLo+y] = baseOffset + bLo + x
			steps++
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x--
		y--
		out[dLo+y] = baseOffset + bLo + x
		steps++
	}
	return true, steps
}

// EncodeDocument returns the bytes a save of LF-normalized text writes in its
// recorded line ending and encoding.
func EncodeDocument(content, eol, encoding string, limits Limits) ([]byte, error) {
	return EncodeBounded(SerializeEOL(content, eol), encoding, limits)
}
