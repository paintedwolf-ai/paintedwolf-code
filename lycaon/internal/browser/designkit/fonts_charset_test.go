package designkit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/lycaon/lycaon/internal/testutil"
)

// woff2CmapUnpacker extracts mapped Unicode codepoints from a WOFF2 font file.
type woff2CmapUnpacker struct {
	chars map[rune]bool
}

func parseWOFF2Cmap(data []byte) (*woff2CmapUnpacker, error) {
	if len(data) < 48 {
		return nil, fmt.Errorf("woff2 data too short (%d bytes)", len(data))
	}
	if string(data[:4]) != "wOF2" {
		return nil, fmt.Errorf("invalid woff2 magic %q", string(data[:4]))
	}

	numTables := binary.BigEndian.Uint16(data[12:14])
	offset := 48

	type tableEntry struct {
		tagIdx          byte
		origLength      uint32
		transformLength uint32
	}

	readBase128 := func() (uint32, error) {
		var res uint32
		for i := 0; i < 5; i++ {
			if offset >= len(data) {
				return 0, io.ErrUnexpectedEOF
			}
			b := data[offset]
			offset++
			res = (res << 7) | uint32(b&0x7f)
			if (b & 0x80) == 0 {
				return res, nil
			}
		}
		return res, nil
	}

	tables := make([]tableEntry, numTables)
	for i := 0; i < int(numTables); i++ {
		if offset >= len(data) {
			return nil, io.ErrUnexpectedEOF
		}
		flags := data[offset]
		offset++
		tagIdx := flags & 0x3f
		if tagIdx == 63 {
			offset += 4 // arbitrary 4-byte tag
		}
		origLen, err := readBase128()
		if err != nil {
			return nil, err
		}
		transformLen := origLen
		if (flags&0xc0) != 0xc0 && (tagIdx == 10 || tagIdx == 11) {
			transformLen, err = readBase128()
			if err != nil {
				return nil, err
			}
		}
		tables[i] = tableEntry{
			tagIdx:          tagIdx,
			origLength:      origLen,
			transformLength: transformLen,
		}
	}

	totalCompressedSize := int(binary.BigEndian.Uint32(data[20:24]))
	if offset+totalCompressedSize > len(data) {
		return nil, fmt.Errorf("compressed stream exceeds data: offset=%d, compressed=%d, len=%d", offset, totalCompressedSize, len(data))
	}

	brotliReader := brotli.NewReader(bytes.NewReader(data[offset : offset+totalCompressedSize]))
	decompressed, err := io.ReadAll(brotliReader)
	if err != nil {
		return nil, fmt.Errorf("brotli decompression failed: %w", err)
	}

	var streamOffset uint32
	var cmapData []byte
	for _, t := range tables {
		size := t.origLength
		if t.transformLength > 0 {
			size = t.transformLength
		}
		if t.tagIdx == 0 { // 0 is known tag 'cmap'
			if int(streamOffset+size) <= len(decompressed) {
				cmapData = decompressed[streamOffset : streamOffset+size]
			}
			break
		}
		streamOffset += size
	}

	if len(cmapData) < 4 {
		return nil, fmt.Errorf("cmap table not found or too short")
	}

	chars := make(map[rune]bool)
	numSubtables := binary.BigEndian.Uint16(cmapData[2:4])
	for i := 0; i < int(numSubtables); i++ {
		entryOffset := 4 + i*8
		if entryOffset+8 > len(cmapData) {
			break
		}
		subOffset := binary.BigEndian.Uint32(cmapData[entryOffset+4 : entryOffset+8])
		if int(subOffset+6) > len(cmapData) {
			continue
		}
		format := binary.BigEndian.Uint16(cmapData[subOffset : subOffset+2])
		if format == 4 {
			if int(subOffset+14) > len(cmapData) {
				continue
			}
			segCount := int(binary.BigEndian.Uint16(cmapData[subOffset+6:subOffset+8])) / 2
			endCodesOffset := int(subOffset) + 14
			startCodesOffset := endCodesOffset + segCount*2 + 2
			if startCodesOffset+segCount*2 > len(cmapData) {
				continue
			}
			for s := 0; s < segCount; s++ {
				endCode := binary.BigEndian.Uint16(cmapData[endCodesOffset+s*2 : endCodesOffset+s*2+2])
				startCode := binary.BigEndian.Uint16(cmapData[startCodesOffset+s*2 : startCodesOffset+s*2+2])
				if startCode <= endCode && endCode != 0xffff {
					for c := startCode; c <= endCode; c++ {
						chars[rune(c)] = true
					}
				}
			}
		} else if format == 12 {
			if int(subOffset+16) > len(cmapData) {
				continue
			}
			numGroups := binary.BigEndian.Uint32(cmapData[subOffset+12 : subOffset+16])
			groupsOffset := int(subOffset) + 16
			for g := 0; g < int(numGroups); g++ {
				grp := groupsOffset + g*12
				if grp+8 > len(cmapData) {
					break
				}
				startChar := binary.BigEndian.Uint32(cmapData[grp : grp+4])
				endChar := binary.BigEndian.Uint32(cmapData[grp+4 : grp+8])
				for c := startChar; c <= endChar; c++ {
					chars[rune(c)] = true
				}
			}
		}
	}

	return &woff2CmapUnpacker{chars: chars}, nil
}

func (u *woff2CmapUnpacker) Has(r rune) bool {
	return u.chars[r]
}

// TestJetBrainsMonoContainsTerminalAndBoxDrawing asserts box-drawing, block element, arrow, and symbol glyph coverage.
func TestJetBrainsMonoContainsTerminalAndBoxDrawing(t *testing.T) {
	data, err := FontFile("JetBrainsMono.woff2")
	testutil.FailErr(t, "read JetBrainsMono.woff2", err)

	unpacker, err := parseWOFF2Cmap(data)
	testutil.FailErr(t, "parse WOFF2 cmap", err)

	requiredBoxDrawing := []struct {
		char rune
		name string
	}{
		{'─', "U+2500 box light horizontal"},
		{'━', "U+2501 box heavy horizontal"},
		{'│', "U+2502 box light vertical"},
		{'┌', "U+250C box light down and right"},
		{'┐', "U+2510 box light down and left"},
		{'└', "U+2514 box light up and right"},
		{'┘', "U+2518 box light up and left"},
		{'┼', "U+253C box light vertical and horizontal"},
		{'═', "U+2550 box double horizontal"},
		{'║', "U+2551 box double vertical"},
	}

	for _, tc := range requiredBoxDrawing {
		if !unpacker.Has(tc.char) {
			t.Errorf("JetBrainsMono.woff2 missing required box-drawing glyph %s (%c)", tc.name, tc.char)
		}
	}

	requiredBlockElements := []struct {
		char rune
		name string
	}{
		{'█', "U+2588 full block"},
		{'▀', "U+2580 upper half block"},
		{'▄', "U+2584 lower half block"},
	}

	for _, tc := range requiredBlockElements {
		if !unpacker.Has(tc.char) {
			t.Errorf("JetBrainsMono.woff2 missing required block element glyph %s (%c)", tc.name, tc.char)
		}
	}

	requiredSymbols := []struct {
		char rune
		name string
	}{
		{'←', "U+2190 leftwards arrow"},
		{'↑', "U+2191 upwards arrow"},
		{'→', "U+2192 rightwards arrow"},
		{'↓', "U+2193 downwards arrow"},
		{'✓', "U+2713 check mark"},
	}

	for _, tc := range requiredSymbols {
		if !unpacker.Has(tc.char) {
			t.Errorf("JetBrainsMono.woff2 missing required symbol glyph %s (%c)", tc.name, tc.char)
		}
	}

	if len(unpacker.chars) < 1000 {
		t.Fatalf("JetBrainsMono.woff2 contains only %d glyphs, expected complete character set (>1000)", len(unpacker.chars))
	}
}

// TestInterContainsCompleteExtendedCharSet asserts Latin Extended, Greek, Cyrillic, and symbol glyph coverage.
func TestInterContainsCompleteExtendedCharSet(t *testing.T) {
	data, err := FontFile("Inter.woff2")
	testutil.FailErr(t, "read Inter.woff2", err)

	unpacker, err := parseWOFF2Cmap(data)
	testutil.FailErr(t, "parse WOFF2 cmap", err)

	requiredGlyphs := []struct {
		char rune
		name string
	}{
		{'Ā', "U+0100 Latin capital A with macron"},
		{'А', "U+0410 Cyrillic capital A"},
		{'Α', "U+0391 Greek capital Alpha"},
		{'→', "U+2192 rightwards arrow"},
		{'✓', "U+2713 check mark"},
	}

	for _, tc := range requiredGlyphs {
		if !unpacker.Has(tc.char) {
			t.Errorf("Inter.woff2 missing required glyph %s (%c)", tc.name, tc.char)
		}
	}

	if len(unpacker.chars) < 2000 {
		t.Fatalf("Inter.woff2 contains only %d glyphs, expected complete character set (>2000)", len(unpacker.chars))
	}
}
