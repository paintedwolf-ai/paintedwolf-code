package fonts

import (
	"testing"

	"golang.org/x/image/font/sfnt"
)

func TestFontsParsed(t *testing.T) {
	jb, inter, err := Parsed()
	if err != nil {
		t.Fatalf("Parsed: %v", err)
	}
	if jb == nil {
		t.Fatal("JetBrains Mono font is nil")
	}
	if inter == nil {
		t.Fatal("Inter font is nil")
	}

	var bfr sfnt.Buffer
	for _, r := range []rune{'A', 'z', '0', '─', '│', '░', '█'} {
		idx, err := jb.GlyphIndex(&bfr, r)
		if err != nil || idx == 0 {
			t.Errorf("JetBrains Mono missing glyph for %c (%U): idx=%d, err=%v", r, r, idx, err)
		}
	}

	for _, r := range []rune{'\u21BB', '\u21BA', '✓'} {
		idx, err := inter.GlyphIndex(&bfr, r)
		if err != nil || idx == 0 {
			t.Errorf("Inter missing glyph for %c (%U): idx=%d, err=%v", r, r, idx, err)
		}
	}
}

func TestReadFaceErrorsOnUnknown(t *testing.T) {
	if _, err := ReadFace("nonexistent.ttf"); err == nil {
		t.Fatal("expected error reading nonexistent face")
	}
}
