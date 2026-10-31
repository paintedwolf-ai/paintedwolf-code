package native

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	dsbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/lycaon/lycaon/internal/testutil"
)

func encodeWith(t *testing.T, format string, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	var w io.WriteCloser
	var err error
	switch format {
	case "tar":
		return plain
	case "tar.gz":
		w = gzip.NewWriter(&out)
	case "tar.bz2":
		w, err = dsbzip2.NewWriter(&out, nil)
	case "tar.xz":
		w, err = xz.NewWriter(&out)
	case "tar.zst":
		w, err = zstd.NewWriter(&out)
	}
	testutil.FailErr(t, format+" writer", err)
	_, err = w.Write(plain)
	testutil.FailErr(t, format+" write", err)
	testutil.FailErr(t, format+" close", w.Close())
	return out.Bytes()
}

func TestArchiveStreamsSniffAndDecodeEveryCodec(t *testing.T) {
	plain := []byte(strings.Repeat("tar stream bytes\n", 64))
	for _, format := range []string{"tar.gz", "tar.bz2", "tar.xz", "tar.zst"} {
		encoded := encodeWith(t, format, plain)
		if got := SniffArchiveFormat(encoded); got != format {
			t.Fatalf("%s sniffed as %s", format, got)
		}
		stream, closeStream, err := OpenArchiveStream(format, bytes.NewReader(encoded), int64(len(encoded)))
		testutil.FailErr(t, "open "+format, err)
		decoded, err := io.ReadAll(stream)
		closeStream()
		testutil.FailErr(t, "decode "+format, err)
		if !bytes.Equal(decoded, plain) {
			t.Fatalf("%s decoded %d bytes", format, len(decoded))
		}
	}
	if got := SniffArchiveFormat(plain); got != "tar" {
		t.Fatalf("plain stream sniffed as %s", got)
	}
}

// xzWithDictionary rewrites the first block's LZMA2 dictionary byte and the
// block header checksum, as a crafted archive would.
func xzWithDictionary(t *testing.T, bits byte) []byte {
	t.Helper()
	data := encodeWith(t, "tar.xz", []byte("small"))
	const streamHeader = 12
	size := (int(data[streamHeader]) + 1) * 4
	header := data[streamHeader : streamHeader+size]
	at := bytes.IndexByte(header[2:], 0x21)
	if at < 0 || header[2+at+1] != 1 {
		t.Fatalf("no LZMA2 filter in block header % x", header)
	}
	header[2+at+2] = bits
	binary.LittleEndian.PutUint32(header[size-4:], crc32.ChecksumIEEE(header[:size-4]))
	return data
}

func TestXZDictionariesAboveTheCeilingRefuseBeforeDecoding(t *testing.T) {
	for _, c := range []struct {
		bits   byte
		refuse bool
	}{{26, false}, {28, false}, {29, true}, {40, true}} {
		data := xzWithDictionary(t, c.bits)
		_, closeStream, err := OpenArchiveStream("tar.xz", bytes.NewReader(data), int64(len(data)))
		if (err != nil) != c.refuse {
			t.Fatalf("dictionary code %d: err=%v, refuse=%v", c.bits, err, c.refuse)
		}
		if err == nil {
			closeStream()
		}
	}
	concatenated := append(append(encodeWith(t, "tar.xz", []byte("one")), 0, 0, 0, 0), xzWithDictionary(t, 30)...)
	if _, _, err := OpenArchiveStream("tar.xz", bytes.NewReader(concatenated), int64(len(concatenated))); err == nil {
		t.Fatal("a later stream's dictionary was not checked")
	}
	for _, broken := range [][]byte{[]byte("\xfd7zXZ\x00garbage"), {}, bytes.Repeat([]byte{0}, 8)} {
		if _, _, err := OpenArchiveStream("tar.xz", bytes.NewReader(broken), int64(len(broken))); err == nil {
			t.Fatalf("malformed xz %q opened", broken)
		}
	}
}
