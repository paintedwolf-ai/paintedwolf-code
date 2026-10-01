package visualscreen

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The invariant this file checks, stated once so every failure repeats it.
const redactionHonestyRule = "a redacted perception must not contain the secret the gate matched; " +
	"withhold perception when redaction cannot remove every occurrence"

const (
	honestySeed  = 0x5eed_7b9b
	honestyCases = 240
)

// honestyPlacement names one way a case plants its secret.
type honestyPlacement string

const (
	placeSVGText        honestyPlacement = "svg text node"
	placeSVGAttr        honestyPlacement = "svg labelling attribute"
	placeSVGComment     honestyPlacement = "svg comment"
	placeSVGCDATA       honestyPlacement = "svg cdata"
	placeSVGSplitTspan  honestyPlacement = "svg text split across adjacent tspans"
	placeSVGSplitRun    honestyPlacement = "svg text split by a comment inside one text element"
	placeSVGEntityHex   honestyPlacement = "svg text with one hex character reference"
	placeSVGEntityAmp   honestyPlacement = "svg text node carrying &amp;"
	placeSVGAttrEntity  honestyPlacement = "svg labelling attribute with a character reference"
	placeSVGDataSVG     honestyPlacement = "svg data: URI of a base64 svg"
	placeSVGDataPNGOCR  honestyPlacement = "svg data: URI of a png read by ocr"
	placeSVGDataPNGMeta honestyPlacement = "svg data: URI of a png with tEXt"
	placePNGtEXt        honestyPlacement = "png tEXt chunk"
	placePNGiTXt        honestyPlacement = "png iTXt chunk"
	placePNGiTXtZ       honestyPlacement = "png compressed iTXt chunk"
	placePNGzTXt        honestyPlacement = "png zTXt chunk"
	placePNGOCR         honestyPlacement = "png pixels read by ocr"
)

var (
	honestySVGPlacements = []honestyPlacement{
		placeSVGText, placeSVGAttr, placeSVGComment, placeSVGCDATA,
		placeSVGSplitTspan, placeSVGSplitRun, placeSVGEntityHex, placeSVGEntityAmp, placeSVGAttrEntity, placeSVGDataSVG,
		placeSVGDataPNGOCR, placeSVGDataPNGMeta,
	}
	honestyPNGPlacements = []honestyPlacement{
		placePNGtEXt, placePNGiTXt, placePNGiTXtZ, placePNGzTXt, placePNGOCR,
	}
)

// pixelOCR is a fake recognizer that reads a planted span only while some
// pixel inside its box is not black, so masking is observable. Images are
// told apart by width, which masking preserves.
type pixelOCR struct {
	mu    sync.Mutex
	spans map[int][]TextSpan
}

func (o *pixelOCR) plant(width int, spans ...TextSpan) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.spans[width] = append(o.spans[width], spans...)
}

func (o *pixelOCR) RecognizeText(_ context.Context, _ string, raw []byte) ([]TextSpan, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("fake ocr decode: %w", err)
	}
	b := img.Bounds()
	o.mu.Lock()
	planted := o.spans[b.Dx()]
	o.mu.Unlock()
	var out []TextSpan
	for _, span := range planted {
		x0 := b.Min.X + int(span.BoundingBox[0]*float64(b.Dx()))
		y0 := b.Min.Y + int(span.BoundingBox[1]*float64(b.Dy()))
		x1 := x0 + int(span.BoundingBox[2]*float64(b.Dx()))
		y1 := y0 + int(span.BoundingBox[3]*float64(b.Dy()))
		if visibleIn(img, image.Rect(x0, y0, x1, y1).Intersect(b)) {
			out = append(out, span)
		}
	}
	return out, nil
}

func visibleIn(img image.Image, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if cr, cg, cb, _ := img.At(x, y).RGBA(); cr|cg|cb != 0 {
				return true
			}
		}
	}
	return false
}

// honestyCase is one generated input.
type honestyCase struct {
	index      int
	secret     string
	mime       string
	raw        []byte
	placements []honestyPlacement
}

func (c honestyCase) describe() string {
	names := make([]string, len(c.placements))
	for i, p := range c.placements {
		names[i] = string(p)
	}
	return fmt.Sprintf("seed=%#x case=%d mime=%s placements=[%s]", honestySeed, c.index, c.mime, strings.Join(names, ", "))
}

type honestyGen struct {
	t     *testing.T
	rng   *rand.Rand
	ocr   *pixelOCR
	width int
}

// nextWidth gives every generated raster a distinct width for pixelOCR.
func (g *honestyGen) nextWidth() int {
	g.width++
	return g.width
}

func (g *honestyGen) awsKey(matcher *secretmatch.Matcher) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	for attempt := 0; attempt < 64; attempt++ {
		var b strings.Builder
		b.WriteString("AKIA")
		for i := 0; i < 16; i++ {
			b.WriteByte(alphabet[g.rng.Intn(len(alphabet))])
		}
		key := b.String()
		if len(matcher.ScreenContext(context.Background(), "key "+key)) > 0 {
			return key
		}
	}
	g.t.Fatal("generator: bundled matcher matched no generated AWS key")
	return ""
}

func (g *honestyGen) pickPlacements(pool []honestyPlacement) []honestyPlacement {
	n := 1 + g.rng.Intn(3)
	out := make([]honestyPlacement, n)
	for i := range out {
		out[i] = pool[g.rng.Intn(len(pool))]
	}
	return out
}

func (g *honestyGen) generate(index int, matcher *secretmatch.Matcher) honestyCase {
	c := honestyCase{index: index, secret: g.awsKey(matcher)}
	if g.rng.Intn(3) == 0 {
		c.mime = "image/png"
		c.placements = g.pickPlacements(honestyPNGPlacements)
		c.raw = g.pngCarrying(c.secret, c.placements)
		return c
	}
	c.mime = "image/svg+xml"
	c.placements = g.pickPlacements(honestySVGPlacements)
	c.raw = g.svgCarrying(c.secret, c.placements, 0)
	return c
}

var honestyFillers = []string{
	`<text x="2" y="12">Public heading</text>`,
	`<rect x="1" y="1" width="8" height="8" fill="#ccc"/>`,
	`<g><text x="4" y="30">Quarterly chart</text></g>`,
	`<circle cx="20" cy="20" r="4"/>`,
}

func (g *honestyGen) svgCarrying(secret string, placements []honestyPlacement, depth int) []byte {
	var parts []string
	for _, p := range placements {
		if g.rng.Intn(2) == 0 {
			parts = append(parts, honestyFillers[g.rng.Intn(len(honestyFillers))])
		}
		parts = append(parts, g.svgPiece(secret, p, depth))
	}
	parts = append(parts, honestyFillers[g.rng.Intn(len(honestyFillers))])
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="120" height="60">` +
		strings.Join(parts, "") + `</svg>`)
}

func (g *honestyGen) svgPiece(secret string, p honestyPlacement, depth int) string {
	prefixes := []string{"", "key ", "token: ", "aws "}
	prefix := prefixes[g.rng.Intn(len(prefixes))]
	switch p {
	case placeSVGText:
		return `<text x="2" y="40">` + prefix + secret + `</text>`
	case placeSVGAttr:
		attrs := []string{"id", "title", "alt", "aria-label"}
		return `<rect ` + attrs[g.rng.Intn(len(attrs))] + `="` + prefix + secret + `" width="4" height="4"/>`
	case placeSVGComment:
		return `<!-- ` + prefix + secret + ` -->`
	case placeSVGCDATA:
		return `<text x="2" y="44"><![CDATA[` + prefix + secret + `]]></text>`
	case placeSVGSplitTspan:
		i := 1 + g.rng.Intn(len(secret)-1)
		seps := []string{"", "\n"}
		return `<text x="2" y="46">` + prefix + `<tspan>` + secret[:i] + `</tspan>` + seps[g.rng.Intn(len(seps))] +
			`<tspan fill="#333">` + secret[i:] + `</tspan></text>`
	case placeSVGSplitRun:
		i := 1 + g.rng.Intn(len(secret)-1)
		return `<text x="2" y="48">` + prefix + secret[:i] + `<!-- split -->` + secret[i:] + `</text>`
	case placeSVGEntityHex:
		return `<text x="2" y="52">` + prefix + g.oneCharRef(secret) + `</text>`
	case placeSVGEntityAmp:
		return `<text x="2" y="56">R&amp;D ` + secret + `</text>`
	case placeSVGAttrEntity:
		return `<rect aria-label="` + g.oneCharRef(secret) + `" width="4" height="4"/>`
	case placeSVGDataSVG:
		inner := []honestyPlacement{placeSVGText, placeSVGComment, placeSVGAttr}
		nested := g.svgCarrying(secret, []honestyPlacement{inner[g.rng.Intn(len(inner))]}, depth+1)
		return `<image href="data:image/svg+xml;base64,` + base64.StdEncoding.EncodeToString(nested) + `" width="20" height="20"/>`
	case placeSVGDataPNGOCR:
		return `<image xlink:href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(g.pngCarrying(secret, []honestyPlacement{placePNGOCR})) + `" width="20" height="20"/>`
	case placeSVGDataPNGMeta:
		return `<image xlink:href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(g.pngCarrying(secret, []honestyPlacement{placePNGtEXt})) + `" width="20" height="20"/>`
	case placePNGtEXt, placePNGiTXt, placePNGiTXtZ, placePNGzTXt, placePNGOCR:
	}
	g.t.Fatalf("generator: unknown svg placement %q", p)
	return ""
}

func (g *honestyGen) oneCharRef(secret string) string {
	i := g.rng.Intn(len(secret))
	return secret[:i] + fmt.Sprintf("&#x%X;", secret[i]) + secret[i+1:]
}

func (g *honestyGen) pngCarrying(secret string, placements []honestyPlacement) []byte {
	width := g.nextWidth()
	img := image.NewRGBA(image.Rect(0, 0, width, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.White)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		testutil.FailErr(g.t, "encode png", err)
	}
	var chunks [][]byte
	ocrRow := 0
	for _, p := range placements {
		text := "note " + secret
		switch p {
		case placePNGtEXt:
			chunks = append(chunks, pngChunk("tEXt", []byte("Comment\x00"+text)))
		case placePNGzTXt:
			chunks = append(chunks, pngChunk("zTXt", append([]byte("Comment\x00\x00"), honestyDeflate(g.t, text)...)))
		case placePNGiTXt:
			chunks = append(chunks, pngChunk("iTXt", []byte("Comment\x00\x00\x00en\x00Comment\x00"+text)))
		case placePNGiTXtZ:
			chunks = append(chunks, pngChunk("iTXt", append([]byte("Comment\x00\x01\x00en\x00Comment\x00"), honestyDeflate(g.t, text)...)))
		case placePNGOCR:
			g.ocr.plant(width,
				TextSpan{Text: "Public heading", Confidence: 1, BoundingBox: [4]float64{0, float64(ocrRow) * 0.25, 0.5, 0.125}},
				TextSpan{Text: text, Confidence: 1, BoundingBox: [4]float64{0.25, float64(ocrRow)*0.25 + 0.125, 0.5, 0.125}},
			)
			ocrRow = (ocrRow + 1) % 4
		default:
			g.t.Fatalf("generator: unknown png placement %q", p)
		}
	}
	raw := buf.Bytes()
	const afterIHDR = 8 + 4 + 4 + 13 + 4
	out := append([]byte(nil), raw[:afterIHDR]...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, raw[afterIHDR:]...)
}

func pngChunk(kind string, data []byte) []byte {
	out := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	copy(out[4:], kind)
	out = append(out, data...)
	crc := crc32.ChecksumIEEE(append([]byte(kind), data...))
	return binary.BigEndian.AppendUint32(out, crc)
}

func honestyDeflate(t *testing.T, s string) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, err := w.Write([]byte(s))
	testutil.FailErr(t, "deflate", err)
	testutil.FailErr(t, "close deflate", w.Close())
	return buf.Bytes()
}

// perceivedText is an oracle independent of the scanner under test: every
// string a renderer or reader of the bytes would surface, including text a
// renderer joins across tspans, decoded character references, and the
// content of embedded data: images.
func perceivedText(t *testing.T, ocr OCREngine, raw []byte, depth int) []string {
	if bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		return perceivedRaster(t, ocr, raw)
	}
	var out []string
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	var run strings.Builder
	inText := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(tk.Name.Local, "text") {
				inText++
			}
			for _, a := range tk.Attr {
				out = append(out, a.Value)
				switch strings.ToLower(a.Name.Local) {
				case "href", "src", "style":
					out = append(out, perceivedRefs(t, ocr, a.Value, depth)...)
				}
			}
		case xml.EndElement:
			if strings.EqualFold(tk.Name.Local, "text") && inText > 0 {
				inText--
				if inText == 0 {
					out = append(out, run.String())
					run.Reset()
				}
			}
		case xml.CharData:
			out = append(out, string(tk))
			if inText > 0 {
				run.Write(tk)
			}
		case xml.Comment:
			out = append(out, string(tk))
		}
	}
	return out
}

var honestyCSSURL = regexp.MustCompile(`url\(\s*['"]?([^'")]*)`)

func perceivedRefs(t *testing.T, ocr OCREngine, value string, depth int) []string {
	refs := []string{value}
	for _, m := range honestyCSSURL.FindAllStringSubmatch(value, -1) {
		refs = append(refs, m[1])
	}
	var out []string
	for _, ref := range refs {
		payload, ok := honestyDataURI(ref)
		if !ok || depth > 4 {
			continue
		}
		out = append(out, string(payload))
		out = append(out, perceivedText(t, ocr, payload, depth+1)...)
	}
	return out
}

func honestyDataURI(ref string) ([]byte, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), "data:") {
		return nil, false
	}
	header, payload, ok := strings.Cut(ref[5:], ",")
	if !ok {
		return nil, false
	}
	if strings.Contains(strings.ToLower(header), ";base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), ""))
		return decoded, err == nil
	}
	decoded, err := url.PathUnescape(payload)
	return []byte(decoded), err == nil
}

func perceivedRaster(t *testing.T, ocr OCREngine, raw []byte) []string {
	var out []string
	for offset := 8; offset+8 <= len(raw); {
		n := int(binary.BigEndian.Uint32(raw[offset:]))
		kind := string(raw[offset+4 : offset+8])
		start := offset + 8
		if n < 0 || start+n > len(raw) {
			break
		}
		data := raw[start : start+n]
		offset = start + n + 4
		switch kind {
		case "tEXt", "iTXt", "zTXt":
			out = append(out, string(data))
			for i := range data {
				if r, err := zlib.NewReader(bytes.NewReader(data[i:])); err == nil {
					var b bytes.Buffer
					_, _ = b.ReadFrom(r)
					out = append(out, b.String())
					break
				}
			}
		}
	}
	spans, err := ocr.RecognizeText(context.Background(), "image/png", raw)
	testutil.FailErr(t, "oracle ocr", err)
	for _, s := range spans {
		out = append(out, s.Text)
	}
	return out
}

// TestVisualGateRedactionHonesty plants one secret in generated SVG and PNG
// inputs, answers every card with send-redacted, and re-reads whatever would
// be perceived with the same matcher.
func TestVisualGateRedactionHonesty(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	ocr := &pixelOCR{spans: map[int][]TextSpan{}}
	gen := &honestyGen{t: t, rng: rand.New(rand.NewSource(honestySeed)), ocr: ocr, width: 40}

	asked := map[honestyPlacement]int{}
	var redactedSends, withheld int
	for i := 0; i < honestyCases; i++ {
		c := gen.generate(i, matcher)
		fired := false
		ask := func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
			fired = true
			return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
		}
		ctx := context.Background()
		outcome, err := NewGate(NewScanner(ocr), matcher, ask).Screen(ctx, VisualScreenInput{
			Mime: c.mime, RawBytes: c.raw, ToolName: "view_image",
		})
		if errors.Is(err, ErrVisualSecretWithheld) {
			withheld++
			continue
		}
		testutil.FailErr(t, "screen "+c.describe(), err)
		if !fired {
			t.Errorf("every planted secret must raise the card: %s\n  screened text: %q", c.describe(), outcome.ExtractedText)
			continue
		}
		for _, p := range c.placements {
			asked[p]++
		}
		if outcome == nil || !outcome.PermitPerception() || len(outcome.PerceiveBytes) == 0 {
			withheld++
			continue
		}
		redactedSends++
		for _, text := range perceivedText(t, ocr, outcome.PerceiveBytes, 0) {
			if hits := matcher.ScreenContext(ctx, text); len(hits) > 0 {
				excerpt := text
				if idx := strings.Index(excerpt, c.secret); idx >= 0 {
					excerpt = excerpt[max(0, idx-40):min(len(excerpt), idx+len(c.secret)+40)]
				} else if len(excerpt) > 160 {
					excerpt = excerpt[:160]
				}
				t.Errorf("%s: %s\n  gate sent redacted bytes (mime %s) that still match rule %s\n  perceived text: %q",
					redactionHonestyRule, c.describe(), outcome.Mime, hits[0].RuleID, excerpt)
				break
			}
		}
	}
	if redactedSends == 0 {
		t.Fatalf("generator produced no redacted send; the property checked nothing (withheld=%d)", withheld)
	}
	t.Logf("cases=%d redacted_sends=%d withheld=%d cards_by_placement=%v", honestyCases, redactedSends, withheld, asked)
}
