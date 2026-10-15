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
	left, top, right, bottom int
	confidence               float64
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
		line.words = append(line.words, text)
		line.left = min(line.left, geom[0])
		line.top = min(line.top, geom[1])
		line.right = max(line.right, geom[0]+geom[2])
		line.bottom = max(line.bottom, geom[1]+geom[3])
		line.confidence += max(conf, 0)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("tesseract tsv: %w", err)
	}
	spans := make([]TextSpan, 0, len(order))
	for _, key := range order {
		line := lines[key]
		spans = append(spans, TextSpan{
			Text:       strings.Join(line.words, " "),
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
