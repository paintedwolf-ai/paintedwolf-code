package zstdcodec_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/lycaon/lycaon/internal/testutil"

	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestCompressDecompressRoundTripsAtDefaultLevel(t *testing.T) {
	want := strings.Repeat("hello zstdcodec ", 1000)
	compressed, err := zstdcodec.Compress(strings.NewReader(want))
	testutil.FailErr(t, "compress", err)
	if len(compressed) >= len(want) {
		t.Fatalf("compressed length %d not smaller than plaintext %d", len(compressed), len(want))
	}
	got, err := zstdcodec.Decompress(compressed)
	testutil.FailErr(t, "decompress", err)
	if string(got) != want {
		t.Fatalf("round trip mismatch: got %d bytes want %d bytes", len(got), len(want))
	}
}

func TestCompressLevelRoundTripsAtNonDefaultLevel(t *testing.T) {
	want := strings.Repeat("compress at a different level ", 1000)
	compressed, err := zstdcodec.CompressLevel(strings.NewReader(want), zstd.SpeedBestCompression)
	testutil.FailErr(t, "compress level", err)
	got, err := zstdcodec.Decompress(compressed)
	testutil.FailErr(t, "decompress", err)
	if string(got) != want {
		t.Fatalf("round trip mismatch: got %d bytes want %d bytes", len(got), len(want))
	}
}

func TestDecompressAcceptsAnyEncoderLevel(t *testing.T) {
	want := strings.Repeat("level independence ", 500)
	fast, err := zstdcodec.CompressLevel(strings.NewReader(want), zstd.SpeedFastest)
	testutil.FailErr(t, "compress fastest", err)
	best, err := zstdcodec.CompressLevel(strings.NewReader(want), zstd.SpeedBestCompression)
	testutil.FailErr(t, "compress best", err)
	for _, z := range [][]byte{fast, best} {
		got, err := zstdcodec.Decompress(z)
		testutil.FailErr(t, "decompress", err)
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("decoded mismatch for input of length %d", len(z))
		}
	}
}

func TestDecompressRejectsGarbageBytes(t *testing.T) {
	if _, err := zstdcodec.Decompress([]byte("not a zstd frame")); err == nil {
		t.Fatal("garbage bytes were accepted as a zstd object")
	}
}

// A bounded decoder must enforce aggregate output across frame boundaries and
// return no usable prefix on resource exhaustion or malformed trailing input.
func TestReadBoundedAggregateFrames(t *testing.T) {
	first, err := zstdcodec.Compress(strings.NewReader(strings.Repeat("a", 600)))
	testutil.FailErr(t, "compress first frame", err)
	second, err := zstdcodec.Compress(strings.NewReader(strings.Repeat("b", 600)))
	testutil.FailErr(t, "compress second frame", err)
	frames := append(append([]byte(nil), first...), second...)
	for _, limit := range []int64{1199, 1200, 1201} {
		out, err := zstdcodec.ReadBounded(bytes.NewReader(frames), limit)
		if limit < 1200 {
			if !errors.Is(err, zstdcodec.ErrDecodedLimit) || out != nil {
				t.Fatalf("limit=%d returned %d bytes, err=%v", limit, len(out), err)
			}
		} else {
			testutil.FailErr(t, "decode at accepted limit", err)
			if string(out) != strings.Repeat("a", 600)+strings.Repeat("b", 600) {
				t.Fatal("decoded output changed")
			}
		}
	}
	out, err := zstdcodec.ReadBounded(bytes.NewReader(append(first, []byte("invalid trailing frame")...)), 1200)
	if err == nil || out != nil {
		t.Fatalf("malformed suffix accepted: %d bytes, err=%v", len(out), err)
	}
}
