package mutation

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	dsbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/ulikunitz/xz"

	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// tarCodecs maps each compression option to its format.
var tarCodecs = []struct{ key, format string }{
	{"-z", "tar.gz"}, {"-j", "tar.bz2"}, {"-J", "tar.xz"}, {"--zstd", "tar.zst"},
}

// codec is the selected compression; with none, extraction and listing
// read the archive's magic bytes and -a creation reads the archive suffix.
func (r *tarRun) codec(head []byte) (string, error) {
	format := ""
	for _, codec := range tarCodecs {
		if r.inv.flag(codec.key) {
			if format != "" {
				return "", r.inv.usage(codec.key, "one compression")
			}
			format = codec.format
		}
	}
	switch {
	case format != "":
		return format, nil
	case r.inv.flag("-c") && r.inv.flag("-a"):
		return autoCompression(r.archive), nil
	case r.inv.flag("-c"):
		return "tar", nil
	}
	return native.SniffArchiveFormat(head), nil
}

// autoCompression chooses the codec -a selects from the archive suffix.
func autoCompression(archive string) string {
	lower := strings.ToLower(archive)
	for _, suffix := range []struct{ suffix, format string }{
		{".tar.gz", "tar.gz"}, {".tgz", "tar.gz"}, {".taz", "tar.gz"},
		{".tar.bz2", "tar.bz2"}, {".tbz", "tar.bz2"}, {".tbz2", "tar.bz2"}, {".tz2", "tar.bz2"},
		{".tar.xz", "tar.xz"}, {".txz", "tar.xz"},
		{".tar.zst", "tar.zst"}, {".tzst", "tar.zst"},
	} {
		if strings.HasSuffix(lower, suffix.suffix) {
			return suffix.format
		}
	}
	return "tar"
}

// compressTar encodes a complete tar stream: gzip at its default level, the
// pinned bzip2 writer, xz with an 8 MiB dictionary, or zstd through the
// host codec.
func compressTar(format string, plain []byte) ([]byte, error) {
	var out bytes.Buffer
	var sink io.WriteCloser
	var err error
	switch format {
	case "tar":
		return plain, nil
	case "tar.gz":
		sink = gzip.NewWriter(&out)
	case "tar.bz2":
		sink, err = dsbzip2.NewWriter(&out, nil)
	case "tar.xz":
		sink, err = xz.NewWriter(&out)
	case "tar.zst":
		return zstdcodec.Compress(bytes.NewReader(plain))
	default:
		return nil, fmt.Errorf("unsupported archive format %q", format)
	}
	if err != nil {
		return nil, err
	}
	if _, err := sink.Write(plain); err != nil {
		return nil, err
	}
	if err := sink.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
