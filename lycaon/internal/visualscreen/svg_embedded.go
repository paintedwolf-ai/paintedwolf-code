package visualscreen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"net/url"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// maxEmbeddedImages bounds how many embedded images one SVG is screened for;
// a document carrying more is reported as not fully screened.
const maxEmbeddedImages = 32

// maxEmbeddedDepth bounds SVG documents nested through data: URIs.
const maxEmbeddedDepth = 2

// cssURLPattern finds url(...) references in style attributes and <style> text.
var cssURLPattern = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]*))\s*\)`)

// svgReferences returns every image reference an SVG can draw: href and src
// attributes on any element, and url(...) values in style attributes and
// <style> text.
func svgReferences(raw []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	var refs []string
	inStyle := 0
	for {
		tok, err := decoder.Token()
		if err != nil {
			return refs
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, "style") {
				inStyle++
			}
			for _, attr := range t.Attr {
				switch strings.ToLower(attr.Name.Local) {
				case "href", "src":
					refs = append(refs, attr.Value)
				case "style":
					refs = append(refs, cssURLs(attr.Value)...)
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "style") && inStyle > 0 {
				inStyle--
			}
		case xml.CharData:
			if inStyle > 0 {
				refs = append(refs, cssURLs(string(t))...)
			}
		}
	}
}

func cssURLs(css string) []string {
	var out []string
	for _, m := range cssURLPattern.FindAllStringSubmatch(css, -1) {
		out = append(out, m[1]+m[2]+m[3])
	}
	return out
}

// dataURI is a decoded data: reference.
type dataURI struct {
	mime  string
	bytes []byte
}

// parseDataURI decodes a data: URI. The decoded bytes are never larger than
// the reference itself, so the SVG's own size bounds them.
func parseDataURI(ref string) (dataURI, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 5 || !strings.EqualFold(ref[:5], "data:") {
		return dataURI{}, false
	}
	header, payload, ok := strings.Cut(ref[5:], ",")
	if !ok {
		return dataURI{}, false
	}
	params := strings.Split(header, ";")
	mime := strings.ToLower(strings.TrimSpace(params[0]))
	encoded := false
	for _, p := range params[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "base64") {
			encoded = true
		}
	}
	if !encoded {
		text, err := url.PathUnescape(payload)
		if err != nil {
			return dataURI{}, false
		}
		return dataURI{mime: mime, bytes: []byte(text)}, true
	}
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, payload)
	decoded, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		if decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(compact, "=")); err != nil {
			return dataURI{}, false
		}
	}
	return dataURI{mime: mime, bytes: decoded}, true
}

// screenEmbedded reads the images an SVG draws. Embedded rasters are screened
// like any raster; their text has no markup range, so a match in it cannot be
// redacted and withholds the pixels. A reference the renderer would load is
// an image the host did not read.
func (s *Scanner) screenEmbedded(ctx context.Context, raw []byte, b *textBuilder, out *ScannedVisual, depth int) {
	images := 0
	for _, ref := range svgReferences(raw) {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.HasPrefix(ref, "#") {
			continue
		}
		data, isData := parseDataURI(ref)
		if !isData {
			if s.rendersReference(ref) {
				out.noteGap(secretmatch.GapEmbeddedReference, "")
			}
			continue
		}
		images++
		if images > maxEmbeddedImages {
			out.noteGap(secretmatch.GapEmbeddedReference, "")
			return
		}
		kind, err := Classify(data.mime, data.bytes)
		switch {
		case err != nil:
			// The renderer may draw a format the screen cannot decode.
			if strings.HasPrefix(data.mime, "image/") {
				out.noteGap(secretmatch.GapEmbeddedReference, "")
			}
		case kind == KindSVG:
			if depth >= maxEmbeddedDepth {
				out.noteGap(secretmatch.GapEmbeddedReference, "")
				continue
			}
			nested := &textBuilder{}
			extractSVGText(data.bytes, nested)
			addEmbeddedLines(b, nested.text.String())
			s.screenEmbedded(ctx, data.bytes, b, out, depth+1)
		default:
			s.screenEmbeddedRaster(ctx, data.bytes, b, out)
		}
	}
}

func (s *Scanner) screenEmbeddedRaster(ctx context.Context, raw []byte, b *textBuilder, out *ScannedVisual) {
	if _, err := CheckRasterBounds(raw); err != nil {
		out.noteGap(secretmatch.GapEmbeddedReference, "")
		return
	}
	for _, line := range extractContainerMetadata(raw) {
		addEmbeddedLines(b, line)
	}
	spans, gap, detail := s.recognize(ctx, rasterMagic(raw), raw)
	if gap != "" {
		out.noteGap(gap, detail)
	}
	for _, span := range spans {
		addEmbeddedLines(b, span.Text)
	}
}

// addEmbeddedLines records text with no source range in the document.
func addEmbeddedLines(b *textBuilder, text string) {
	for _, line := range strings.Split(text, "\n") {
		b.add(segment{kind: segmentEmbedded, rawStart: -1, span: -1}, line)
	}
}

// rendersReference reports whether the renderer would load the reference.
// Without a declared rule every reference counts as loadable.
func (s *Scanner) rendersReference(ref string) bool {
	if s == nil || s.renderLoads == nil {
		return true
	}
	return s.renderLoads(ref)
}

// noteGap keeps the first gap the screen met.
func (v *ScannedVisual) noteGap(gap secretmatch.ScreeningGap, detail string) {
	if v.Gap == "" {
		v.Gap, v.GapDetail = gap, detail
	}
}
