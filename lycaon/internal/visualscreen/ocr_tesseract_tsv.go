package visualscreen

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// tsvWordLevel is the row level of one recognized word in Tesseract TSV.
const tsvWordLevel = 5

var errTSVHeader = errors.New("tesseract tsv header missing")

// tsvLine accumulates the words of one recognized line.
type tsvLine struct {
	words                    []string
	wordSlots                map[string]int
	wordAlts                 [][]string
	left, top, right, bottom int
	confidence               float64
}

func (l *tsvLine) addWord(wordNum, text string, conf float64) {
	if l.wordSlots == nil {
		l.wordSlots = map[string]int{}
	}
	if slot, exists := l.wordSlots[wordNum]; exists {
		if l.words[slot] == text {
			return
		}
		for _, alt := range l.wordAlts[slot] {
			if alt == text {
				return
			}
		}
		l.wordAlts[slot] = append(l.wordAlts[slot], text)
		return
	}
	slot := len(l.words)
	l.wordSlots[wordNum] = slot
	l.words = append(l.words, text)
	l.wordAlts = append(l.wordAlts, nil)
	l.confidence += max(conf, 0)
}

func (l *tsvLine) candidates() []string {
	primary := strings.Join(l.words, " ")
	seen := map[string]struct{}{primary: {}}
	var out []string
	add := func(s string) {
		if s == "" || len(out) >= maxOCRCandidates-1 {
			return
		}
		if _, dup := seen[s]; dup {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for slot, alts := range l.wordAlts {
		for _, alt := range alts {
			words := append([]string(nil), l.words...)
			words[slot] = alt
			add(strings.Join(words, " "))
		}
	}
	if joined, ok := joinKerningSplitWords(l.words); ok {
		add(joined)
		for _, collapsed := range collapseOCRDoublets(joined) {
			add(collapsed)
		}
	}
	for _, collapsed := range collapseOCRDoublets(primary) {
		add(collapsed)
	}
	return out
}

// joinKerningSplitWords rejoins single tokens that Tesseract split into
// adjacent words around kerning gaps or assignment operators.
func joinKerningSplitWords(words []string) (string, bool) {
	if len(words) < 2 {
		return "", false
	}
	merged := make([]string, 0, len(words))
	merged = append(merged, words[0])
	changed := false
	for i := 1; i < len(words); i++ {
		prev := merged[len(merged)-1]
		next := words[i]
		if shouldJoinOCRWords(prev, next) {
			merged[len(merged)-1] = prev + next
			changed = true
			continue
		}
		merged = append(merged, next)
	}
	if !changed {
		return "", false
	}
	return strings.Join(merged, " "), true
}

func shouldJoinOCRWords(prev, next string) bool {
	if prev == "" || next == "" {
		return false
	}
	if strings.HasSuffix(prev, "=") || strings.HasPrefix(next, "=") {
		return true
	}
	if looksLikeTokenFragment(prev) && looksLikeTokenFragment(next) {
		return true
	}
	last := prev[len(prev)-1]
	switch last {
	case 'T', 'Y', 'F', 'V', 'W', '/':
		return isTokenChar(next[0]) && (looksLikeTokenFragment(prev) || looksLikeTokenFragment(next))
	}
	return false
}

func looksLikeTokenFragment(s string) bool {
	if strings.ContainsAny(s, "=_") {
		return true
	}
	hasDigit, hasLetter := false, false
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= '0' && b <= '9' {
			hasDigit = true
		} else if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

func isTokenChar(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_' || b == '|' || b == '!' || b == '/'
}

var ocrGlyphDoublets = [][2]string{
	{"/7", "7"},
	{"7/", "7"},
	{"S5", "5"},
	{"5S", "5"},
	{"O0", "O"},
	{"0O", "0"},
	{"1I", "1"},
	{"I1", "I"},
	{"1l", "1"},
	{"l1", "l"},
	{"lI", "I"},
	{"Il", "I"},
}

// collapseOCRDoublets collapses adjacent confusion-pair doublets produced when
// Tesseract over-segments a single wide glyph into two characters.
func collapseOCRDoublets(s string) []string {
	var out []string
	all := s
	for _, pair := range ocrGlyphDoublets {
		if idx := strings.Index(s, pair[0]); idx >= 0 {
			out = append(out, s[:idx]+pair[1]+s[idx+len(pair[0]):])
			all = strings.ReplaceAll(all, pair[0], pair[1])
		}
	}
	if all != s {
		out = append(out, all)
	}
	return out
}

// parseTesseractTSV groups word rows into line spans. Boxes are normalized to
// the recognized raster's size; confidence is the mean word confidence on [0,1].
func parseTesseractTSV(data []byte, width, height int) ([]TextSpan, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("tesseract tsv: raster size %dx%d", width, height)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	if !scanner.Scan() {
		return nil, errTSVHeader
	}
	if fields := strings.Split(scanner.Text(), "\t"); len(fields) != 12 || fields[0] != "level" || fields[11] != "text" {
		return nil, errTSVHeader
	}
	var (
		order []string
		lines = map[string]*tsvLine{}
	)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 12 {
			return nil, fmt.Errorf("tesseract tsv: row has %d fields", len(fields))
		}
		level, err := strconv.Atoi(fields[0])
		if err != nil || level != tsvWordLevel {
			continue
		}
		text := strings.TrimSpace(fields[11])
		if text == "" {
			continue
		}
		var geom [4]int
		for i, f := range fields[6:10] {
			if geom[i], err = strconv.Atoi(f); err != nil {
				return nil, fmt.Errorf("tesseract tsv: geometry %q", f)
			}
		}
		conf, err := strconv.ParseFloat(fields[10], 64)
		if err != nil {
			return nil, fmt.Errorf("tesseract tsv: confidence %q", fields[10])
		}
		key := strings.Join(fields[1:5], ":")
		line, ok := lines[key]
		if !ok {
			line = &tsvLine{left: geom[0], top: geom[1], right: geom[0] + geom[2], bottom: geom[1] + geom[3]}
			lines[key] = line
			order = append(order, key)
		}
		line.addWord(fields[5], text, conf)
		line.left = min(line.left, geom[0])
		line.top = min(line.top, geom[1])
		line.right = max(line.right, geom[0]+geom[2])
		line.bottom = max(line.bottom, geom[1]+geom[3])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("tesseract tsv: %w", err)
	}
	spans := make([]TextSpan, 0, len(order))
	for _, key := range order {
		line := lines[key]
		spans = append(spans, TextSpan{
			Text:       strings.Join(line.words, " "),
			Candidates: line.candidates(),
			Confidence: line.confidence / float64(len(line.words)) / 100,
			BoundingBox: [4]float64{
				float64(line.left) / float64(width),
				float64(line.top) / float64(height),
				float64(line.right-line.left) / float64(width),
				float64(line.bottom-line.top) / float64(height),
			},
		})
	}
	return spans, nil
}
