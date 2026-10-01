package visualscreen

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExtractSVGText(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100">
		<title>System Architecture</title>
		<g id="component-node">
			<text x="10" y="20">Database Cluster</text>
		</g>
		<!-- api-key-comment -->
	</svg>`
	b := &textBuilder{}
	extractSVGText([]byte(svg), b)
	joined := b.text.String()
	for _, want := range []string{"System Architecture", "component-node", "Database Cluster", "api-key-comment"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	for _, seg := range b.segments {
		if seg.rawStart < 0 {
			t.Errorf("verbatim segment lost its source range: %+v", seg)
			continue
		}
		if got, want := svg[seg.rawStart:seg.rawEnd], string([]rune(joined)[seg.start:seg.end]); got != want {
			t.Errorf("source range %q != extracted %q", got, want)
		}
	}
}

// Text one <text> element renders contiguously is read as one line, with a
// source range per piece; a tspan with y or dy starts a new line.
func TestExtractSVGTextJoinsRenderedRuns(t *testing.T) {
	svg := `<svg><text x="1">AK<tspan>IA</tspan>
<tspan fill="red">QY</tspan><!-- c -->JK  <tspan>5T</tspan><title>tip</title><tspan dy="1em">next line</tspan></text>` +
		`<text xml:space="preserve">a
b</text></svg>`
	b := &textBuilder{}
	extractSVGText([]byte(svg), b)
	lines := strings.Split(b.text.String(), "\n")
	want := []string{"c", "tip", "AKIAQYJK 5T", "next line", "a b"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	joined := []rune(b.text.String())
	for _, seg := range b.segments {
		if seg.rawStart < 0 {
			t.Fatalf("verbatim piece lost its source range: %+v", seg)
		}
		if got, want := svg[seg.rawStart:seg.rawEnd], string(joined[seg.start:seg.end]); got != want {
			t.Errorf("source range %q != extracted %q", got, want)
		}
	}
}

// pngWithChunk inserts one ancillary chunk before IEND.
func pngWithChunk(t *testing.T, chunkType string, data []byte) []byte {
	t.Helper()
	raw := whitePNG(t, 2, 2)
	iend := bytes.Index(raw, []byte("IEND")) - 4
	var out bytes.Buffer
	out.Write(raw[:iend])
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	out.Write(length[:])
	out.WriteString(chunkType)
	out.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(chunkType))
	crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	out.Write(sum[:])
	out.Write(raw[iend:])
	return out.Bytes()
}

func TestExtractPNGMetadata(t *testing.T) {
	raw := pngWithChunk(t, "tEXt", []byte("Author\x00Painted Wolf Code Team"))
	if joined := strings.Join(extractPNGMetadata(raw), " "); !strings.Contains(joined, "Painted Wolf Code Team") {
		t.Errorf("expected tEXt chunk metadata, got %q", joined)
	}
}

func TestExtractPNGITXt(t *testing.T) {
	data := []byte("Comment\x00\x00\x00en\x00Kommentar\x00plain itxt body")
	if joined := strings.Join(extractPNGMetadata(pngWithChunk(t, "iTXt", data)), " "); !strings.Contains(joined, "plain itxt body") {
		t.Errorf("iTXt text missing: %q", joined)
	}
}

// A small compressed chunk cannot inflate into an unbounded allocation.
func TestExtractPNGZTXtInflateIsBounded(t *testing.T) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(bytes.Repeat([]byte("A"), 8<<20)); err != nil {
		testutil.FailErr(t, "compress", err)
	}
	if err := zw.Close(); err != nil {
		testutil.FailErr(t, "close zlib", err)
	}
	data := append([]byte("Comment\x00\x00"), z.Bytes()...)
	lines := extractPNGMetadata(pngWithChunk(t, "zTXt", data))
	if len(lines) != 1 || len(lines[0]) != MaxInflatedTextBytes {
		t.Fatalf("inflated %d lines, first len %d; want one line of %d bytes", len(lines), len(lines[0]), MaxInflatedTextBytes)
	}
}

// Raster magic wins over an SVG sniff in the first bytes.
func TestClassifyTrustsRasterMagic(t *testing.T) {
	raw := pngWithChunk(t, "tEXt", []byte("Comment\x00<svg onload=x>"))
	kind, err := Classify("image/svg+xml", raw)
	if err != nil || kind != KindRaster {
		t.Fatalf("kind = %q, err = %v", kind, err)
	}
	if kind, err := Classify("image/png", []byte(`<svg></svg>`)); err != nil || kind != KindSVG {
		t.Fatalf("svg kind = %q, err = %v", kind, err)
	}
	if _, err := Classify("image/png", []byte("not an image")); !errors.Is(err, ErrImageUndecodable) {
		t.Fatalf("garbage err = %v", err)
	}
}

func TestScanReportsOCRCoverage(t *testing.T) {
	scanned, err := NewScanner(&fakeOCR{err: ErrOCRUnavailable}).Scan(context.Background(), "image/png", whitePNG(t, 2, 2))
	if err != nil {
		testutil.FailErr(t, "scan", err)
	}
	if scanned.Gap == "" {
		t.Fatal("unavailable OCR reported full coverage")
	}
	scanned, err = NewScanner(&fakeOCR{spans: []TextSpan{{Text: "Server OK"}}}).Scan(context.Background(), "image/png", whitePNG(t, 2, 2))
	if err != nil {
		testutil.FailErr(t, "scan ok", err)
	}
	if scanned.Gap != "" || scanned.ExtractedText != "Server OK" {
		t.Fatalf("scanned = %+v", scanned)
	}
}

func TestScannerSVGScan(t *testing.T) {
	scanned, err := NewScanner(nil).Scan(context.Background(), "image/svg+xml", []byte(`<svg width="50" height="50"><text>Server OK</text></svg>`))
	if err != nil {
		testutil.FailErr(t, "scan", err)
	}
	if !strings.Contains(scanned.ExtractedText, "Server OK") || scanned.Gap != "" {
		t.Errorf("scanned = %+v", scanned)
	}
}
