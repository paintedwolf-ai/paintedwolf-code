package textfile

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type byteAtATime struct{ io.Reader }

func (r byteAtATime) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(1, len(p))]) }

func TestUTF8ReaderPreservesScalarsAcrossEveryByteBoundary(t *testing.T) {
	text := "é😀\r\n世界\uFFFD"
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LE, UTF16LEBOM, UTF16BE, UTF16BEBOM} {
		raw, ok := encodeUnchecked(text, encoding)
		if !ok {
			t.Fatal(encoding)
		}
		for _, chunk := range []int{1, 2, 3, 5, 4096} {
			reader, err := NewUTF8Reader(byteAtATime{bytes.NewReader(raw)}, encoding)
			testutil.FailErr(t, "NewUTF8Reader failed", err)
			var out bytes.Buffer
			buffer := make([]byte, chunk)
			for {
				n, err := reader.Read(buffer)
				out.Write(buffer[:n])
				if errors.Is(err, io.EOF) {
					break
				}
				testutil.FailErr(t, "ors.Is failed", err)
			}
			if out.String() != text {
				t.Fatalf("%s chunk %d text %q", encoding, chunk, out.String())
			}
		}
	}
}

func TestUTF8ReaderRefusesInvalidOrIncompleteEncoding(t *testing.T) {
	for _, raw := range [][]byte{{0xff}, {0xf0, 0x9f}, {0xe2, 'x', 0x80}, {'x', 0, 'y'}} {
		reader, err := NewUTF8Reader(byteAtATime{bytes.NewReader(raw)}, UTF8)
		testutil.FailErr(t, "NewUTF8Reader failed", err)
		_, err = io.ReadAll(reader)
		if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrBinary) {
			t.Fatalf("%x accepted: %v", raw, err)
		}
	}
}
