package format

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Container is a single-body compression wrapper.
type Container string

const (
	ContainerGzip  Container = "gzip"
	ContainerZstd  Container = "zstd"
	ContainerXz    Container = "xz"
	ContainerBzip2 Container = "bzip2"
)

var containerMagic = []struct {
	name  Container
	magic []byte
}{
	{ContainerGzip, []byte{0x1f, 0x8b}},
	{ContainerZstd, []byte{0x28, 0xb5, 0x2f, 0xfd}},
	{ContainerXz, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}},
	{ContainerBzip2, []byte{'B', 'Z', 'h'}},
}

func detectContainer(peek []byte) (Container, bool) {
	for _, c := range containerMagic {
		if bytes.HasPrefix(peek, c.magic) {
			return c.name, true
		}
	}
	return "", false
}

// Decode returns a streaming decompressor.
func (c Container) Decode(r io.Reader) (io.ReadCloser, error) {
	switch c {
	case ContainerGzip:
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return zr, nil
	case ContainerZstd:
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		return zr.IOReadCloser(), nil
	case ContainerXz:
		xr, err := xz.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("xz: %w", err)
		}
		return io.NopCloser(xr), nil
	case ContainerBzip2:
		return io.NopCloser(bzip2.NewReader(r)), nil
	}
	return nil, fmt.Errorf("unknown container %q", string(c))
}

// InnerName strips one supported compression suffix.
func (c Container) InnerName(filename string) string {
	base := strings.TrimSpace(filename)
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".gz", ".zst", ".xz", ".bz2", ".bzip2", ".zstd":
		return strings.TrimSuffix(base, path.Ext(base))
	}
	return base
}
