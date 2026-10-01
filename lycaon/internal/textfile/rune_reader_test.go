package textfile

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRuneReaderPreservesEverySupportedEncoding(t *testing.T) {
	want := strings.Repeat("a", 65535) + "🦊\r\n日本語"
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LE, UTF16LEBOM, UTF16BE, UTF16BEBOM} {
		t.Run(encoding, func(t *testing.T) {
			raw, ok := encodeUnchecked(want, encoding)
			if !ok {
				t.Fatal("could not encode fixture")
			}
			reader, err := NewRuneReader(bytes.NewReader(raw), encoding)
			testutil.FailErr(t, "open streaming decoder", err)
			var got strings.Builder
			for {
				value, _, err := reader.ReadRune()
				if errors.Is(err, io.EOF) {
					break
				}
				testutil.FailErr(t, "decode rune", err)
				got.WriteRune(value)
			}
			if got.String() != want {
				t.Fatal("streaming decoder changed text")
			}
		})
	}
}

func TestRuneReaderRejectsMalformedTail(t *testing.T) {
	for _, tc := range []struct {
		encoding string
		raw      []byte
	}{
		{UTF8, []byte{0xf0, 0x9f}}, {UTF16BE, []byte{0xd8, 0x00}}, {UTF16LE, []byte{0x00}}, {UTF16LE, []byte{0x00, 0xdc}},
	} {
		reader, err := NewRuneReader(bytes.NewReader(tc.raw), tc.encoding)
		testutil.FailErr(t, "open malformed stream", err)
		if _, _, err := reader.ReadRune(); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("malformed %s error = %v", tc.encoding, err)
		}
	}
}

func TestRuneReaderRejectsNULAfterSniffPrefix(t *testing.T) {
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LE, UTF16LEBOM, UTF16BE, UTF16BEBOM} {
		t.Run(encoding, func(t *testing.T) {
			raw, ok := encodeUnchecked(strings.Repeat("a", 16384)+"\x00", encoding)
			if !ok {
				t.Fatal("could not encode binary fixture")
			}
			reader, err := NewRuneReader(bytes.NewReader(raw), encoding)
			testutil.FailErr(t, "open streaming decoder", err)
			for range 16384 {
				_, _, err := reader.ReadRune()
				testutil.FailErr(t, "decode prefix", err)
			}
			if _, _, err := reader.ReadRune(); !errors.Is(err, ErrBinary) {
				t.Fatalf("NUL in %s error = %v, want binary rejection", encoding, err)
			}
		})
	}
}
