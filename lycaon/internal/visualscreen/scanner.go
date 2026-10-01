package visualscreen

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// MaxInflatedTextBytes bounds one compressed PNG text chunk after inflation.
// The screen reads text, not payloads; a chunk larger than the model-facing
// text budget of a visual result carries nothing the screen can present.
const MaxInflatedTextBytes = 64 << 10

// ErrImageUndecodable reports raster bytes whose header does not decode.
var ErrImageUndecodable = errors.New("image header does not decode")

// DimensionsError reports a raster larger than the perception bound.
type DimensionsError struct {
	Width, Height int
	Max           int
}

func (e *DimensionsError) Error() string {
	return fmt.Sprintf("image %dx%d exceeds %d pixels on an edge", e.Width, e.Height, e.Max)
}

// Kind is the byte-level class of a visual.
type Kind string

const (
	KindRaster Kind = "raster"
	KindSVG    Kind = "svg"
)

// Classify trusts raster magic bytes before any declared MIME, and treats
// bytes as SVG only when no raster signature is present.
func Classify(mime string, raw []byte) (Kind, error) {
	if rasterMagic(raw) != "" {
		return KindRaster, nil
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "image/svg+xml" || mime == "image/svg" || looksLikeSVG(raw) {
		return KindSVG, nil
	}
	return "", ErrImageUndecodable
}

func rasterMagic(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(raw, []byte("\xff\xd8\xff")):
		return "jpeg"
	case bytes.HasPrefix(raw, []byte("GIF87a")), bytes.HasPrefix(raw, []byte("GIF89a")):
		return "gif"
	case len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "webp"
	default:
		return ""
	}
}

func looksLikeSVG(raw []byte) bool {
	head := bytes.ToLower(raw[:min(len(raw), 512)])
	return bytes.Contains(head, []byte("<svg"))
}

// RasterHeader is what a raster header states without decoding pixels.
type RasterHeader struct {
	Width, Height int
	// Format is the decoder name: png, jpeg, gif, or webp.
	Format string
}

// CheckRasterBounds decodes only the raster header and refuses an image whose
// longest edge exceeds the perception bound. Every path runs it before a full
// decode, OCR, or screening.
func CheckRasterBounds(raw []byte) (RasterHeader, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return RasterHeader{}, fmt.Errorf("%w: %w", ErrImageUndecodable, err)
	}
	header := RasterHeader{Width: cfg.Width, Height: cfg.Height, Format: format}
	if cfg.Width > providerwire.RejectImageDimension || cfg.Height > providerwire.RejectImageDimension {
		return header, &DimensionsError{Width: cfg.Width, Height: cfg.Height, Max: providerwire.RejectImageDimension}
	}
	return header, nil
}

// segmentKind names where a piece of screened text came from.
type segmentKind int

const (
	segmentMarkup segmentKind = iota
	segmentMetadata
	segmentOCR
	// segmentEmbedded is text from an image an SVG draws; it has no range in
	// the document, so a match in it cannot be redacted there.
	segmentEmbedded
)

// segment is one extracted line with its rune range in the screened text.
// Markup segments keep the source byte range the text was read from; rawStart
// is -1 when the decoded text is not a verbatim slice of the source.
type segment struct {
	kind             segmentKind
	start, end       int
	rawStart, rawEnd int
	span             int
}

// ScannedVisual is the text the screen can read from a visual and the facts
// needed to redact a match in the bytes that produced it.
type ScannedVisual struct {
	Kind          Kind
	ExtractedText string
	Spans         []TextSpan
	// Gap names text the screen could not read; empty when coverage is complete.
	Gap secretmatch.ScreeningGap
	// GapDetail is the engine error behind GapOCRFailed.
	GapDetail string
	segments  []segment
}

// Scanner extracts text from SVG markup, raster metadata, and OCR.
type Scanner struct {
	ocr OCREngine
	// renderLoads reports whether the SVG renderer loads a reference; nil
	// treats every reference as loadable.
	renderLoads func(ref string) bool
}

// NewScanner creates a visual scanner; nil uses the platform engine.
func NewScanner(ocr OCREngine) *Scanner {
	if ocr == nil {
		ocr = NewDefaultOCREngine()
	}
	return &Scanner{ocr: ocr}
}

// WithRenderedReferences declares which references the SVG renderer loads,
// so a reference it blocks is not reported as an unread image.
func (s *Scanner) WithRenderedReferences(loads func(ref string) bool) *Scanner {
	s.renderLoads = loads
	return s
}

// Scan reads the text a visual carries. A raster must pass CheckRasterBounds
// first; OCR outcomes are returned as coverage facts, never swallowed.
func (s *Scanner) Scan(ctx context.Context, mime string, raw []byte) (*ScannedVisual, error) {
	kind, err := Classify(mime, raw)
	if err != nil {
		return nil, err
	}
	b := &textBuilder{}
	out := &ScannedVisual{Kind: kind}
	switch kind {
	case KindSVG:
		extractSVGText(raw, b)
		s.screenEmbedded(ctx, raw, b, out, 0)
	case KindRaster:
		if _, err := CheckRasterBounds(raw); err != nil {
			return nil, err
		}
		for _, line := range extractContainerMetadata(raw) {
			b.add(segment{kind: segmentMetadata, rawStart: -1, span: -1}, line)
		}
		spans, gap, detail := s.recognize(ctx, rasterMagic(raw), raw)
		out.Spans, out.Gap, out.GapDetail = spans, gap, detail
		for i, span := range spans {
			b.add(segment{kind: segmentOCR, rawStart: -1, span: i}, span.Text)
		}
	}
	out.ExtractedText = b.text.String()
	out.segments = b.segments
	return out, nil
}

func (s *Scanner) recognize(ctx context.Context, format string, raw []byte) ([]TextSpan, secretmatch.ScreeningGap, string) {
	if s == nil || s.ocr == nil {
		return nil, secretmatch.GapOCRUnavailable, ""
	}
	spans, err := s.ocr.RecognizeText(ctx, "image/"+format, raw)
	switch {
	case errors.Is(err, ErrOCRUnavailable):
		return nil, secretmatch.GapOCRUnavailable, ""
	case err != nil:
		return nil, secretmatch.GapOCRFailed, err.Error()
	}
	return spans, "", ""
}

// textBuilder joins extracted lines with newlines and records rune ranges.
type textBuilder struct {
	text     strings.Builder
	runes    int
	segments []segment
}

// add appends one line; surrounding whitespace is trimmed and the raw range,
// when known, narrows with it.
func (b *textBuilder) add(seg segment, line string) {
	trimmedLeft := strings.TrimLeft(line, " \t\r\n")
	trimmed := strings.TrimRight(trimmedLeft, " \t\r\n")
	if trimmed == "" {
		return
	}
	if seg.rawStart >= 0 {
		seg.rawStart += len(line) - len(trimmedLeft)
		seg.rawEnd = seg.rawStart + len(trimmed)
	}
	if b.runes > 0 {
		b.text.WriteByte('\n')
		b.runes++
	}
	n := len([]rune(trimmed))
	seg.start, seg.end = b.runes, b.runes+n
	b.text.WriteString(trimmed)
	b.runes += n
	b.segments = append(b.segments, seg)
}

// addRun appends one line joined from rendered pieces, one markup segment per
// piece, so a match spanning pieces maps to each piece's source range.
func (b *textBuilder) addRun(parts []runPart) {
	if len(parts) == 0 {
		return
	}
	if b.runes > 0 {
		b.text.WriteByte('\n')
		b.runes++
	}
	for _, part := range parts {
		if part.spaceBefore {
			b.text.WriteByte(' ')
			b.runes++
		}
		seg := segment{kind: segmentMarkup, rawStart: part.rawStart, span: -1}
		if part.rawStart >= 0 {
			seg.rawEnd = part.rawStart + len(part.text)
		}
		n := len([]rune(part.text))
		seg.start, seg.end = b.runes, b.runes+n
		b.text.WriteString(part.text)
		b.runes += n
		b.segments = append(b.segments, seg)
	}
}

func extractContainerMetadata(raw []byte) []string {
	switch rasterMagic(raw) {
	case "png":
		return extractPNGMetadata(raw)
	case "jpeg":
		return extractJPEGMetadata(raw)
	case "gif":
		return extractGIFMetadata(raw)
	default:
		return nil
	}
}

func extractPNGMetadata(raw []byte) []string {
	var lines []string
	if len(raw) < 8 || !bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil
	}
	offset := 8
	for offset+8 <= len(raw) {
		chunkLen := int(binary.BigEndian.Uint32(raw[offset : offset+4]))
		chunkType := string(raw[offset+4 : offset+8])
		offset += 8
		if chunkLen < 0 || offset+chunkLen > len(raw) {
			break
		}
		chunkData := raw[offset : offset+chunkLen]
		offset += chunkLen + 4

		switch chunkType {
		case "tEXt":
			parts := bytes.SplitN(chunkData, []byte{0}, 2)
			if len(parts) == 2 {
				lines = append(lines, string(parts[1]))
			}
		case "zTXt":
			// Keyword, NUL, compression method, compressed text.
			parts := bytes.SplitN(chunkData, []byte{0}, 2)
			if len(parts) == 2 && len(parts[1]) > 1 {
				if text, ok := inflateText(parts[1][1:]); ok {
					lines = append(lines, text)
				}
			}
		case "iTXt":
			// Keyword, NUL, flag, method, language tag, NUL, translated keyword, NUL, text.
			parts := bytes.SplitN(chunkData, []byte{0}, 2)
			if len(parts) != 2 || len(parts[1]) < 2 {
				continue
			}
			compressed := parts[1][0] == 1
			rest := bytes.SplitN(parts[1][2:], []byte{0}, 3)
			if len(rest) != 3 {
				continue
			}
			content := rest[2]
			if compressed {
				text, ok := inflateText(content)
				if !ok {
					continue
				}
				lines = append(lines, text)
				continue
			}
			lines = append(lines, string(content))
		case "IEND":
			return lines
		}
	}
	return lines
}

// inflateText reads at most MaxInflatedTextBytes of a zlib stream; longer
// text is truncated at the bound.
func inflateText(compressed []byte) (string, bool) {
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", false
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, MaxInflatedTextBytes))
	if err != nil && len(out) == 0 {
		return "", false
	}
	return string(out), true
}

func extractJPEGMetadata(raw []byte) []string {
	var lines []string
	if len(raw) < 4 || raw[0] != 0xff || raw[1] != 0xd8 {
		return nil
	}
	offset := 2
	for offset+4 <= len(raw) {
		if raw[offset] != 0xff {
			offset++
			continue
		}
		marker := raw[offset+1]
		offset += 2
		if marker == 0xd9 || marker == 0xda {
			break
		}
		if marker == 0x00 || (marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if offset+2 > len(raw) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(raw[offset : offset+2]))
		if segLen < 2 || offset+segLen > len(raw) {
			break
		}
		segData := raw[offset+2 : offset+segLen]
		offset += segLen

		switch marker {
		case 0xfe:
			lines = append(lines, string(segData))
		case 0xe1:
			const xmpHeader = "http://ns.adobe.com/xap/1.0/\x00"
			if bytes.HasPrefix(segData, []byte(xmpHeader)) {
				b := &textBuilder{}
				extractSVGText(segData[len(xmpHeader):], b)
				if text := b.text.String(); text != "" {
					lines = append(lines, strings.Split(text, "\n")...)
				}
			}
		}
	}
	return lines
}

func extractGIFMetadata(raw []byte) []string {
	var lines []string
	if len(raw) < 13 || !bytes.HasPrefix(raw, []byte("GIF8")) {
		return nil
	}
	offset := 13
	if raw[10]&0x80 != 0 {
		offset += 3 * (1 << ((raw[10] & 0x07) + 1))
	}
	for offset < len(raw) {
		b := raw[offset]
		if b == 0x3b {
			break
		}
		if b == 0x21 {
			if offset+2 > len(raw) {
				break
			}
			extType := raw[offset+1]
			offset += 2
			var blockData []byte
			for offset < len(raw) {
				subLen := int(raw[offset])
				offset++
				if subLen == 0 || offset+subLen > len(raw) {
					break
				}
				blockData = append(blockData, raw[offset:offset+subLen]...)
				offset += subLen
			}
			if extType == 0xfe {
				lines = append(lines, string(blockData))
			}
			continue
		}
		offset++
	}
	return lines
}
