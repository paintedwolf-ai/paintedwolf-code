package native

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ulikunitz/xz"

	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// MaxXZDictionary is the largest xz dictionary a tar stream may declare: the
// size xz -9 writes. The decoder allocates the dictionary a block names
// before reading it, so every block header is checked first.
const MaxXZDictionary = 64 << 20

// SniffArchiveFormat names a tar stream's compression from its magic bytes.
func SniffArchiveFormat(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "tar.gz"
	case bytes.HasPrefix(head, []byte("BZh")):
		return "tar.bz2"
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		return "tar.xz"
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "tar.zst"
	}
	return "tar"
}

// OpenArchiveStream decodes the tar stream held in src under format. The
// caller bounds the decoded bytes; close releases decoder resources.
func OpenArchiveStream(format string, src io.ReaderAt, size int64) (stream io.Reader, close func(), err error) {
	raw := io.NewSectionReader(src, 0, size)
	switch format {
	case "tar":
		return raw, func() {}, nil
	case "tar.gz":
		gz, err := gzip.NewReader(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip: %w", err)
		}
		return gz, func() { _ = gz.Close() }, nil
	case "tar.bz2":
		return bzip2.NewReader(raw), func() {}, nil
	case "tar.xz":
		if err := checkXZDictionaries(src, size); err != nil {
			return nil, nil, err
		}
		x, err := xz.NewReader(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("open xz: %w", err)
		}
		return x, func() {}, nil
	case "tar.zst":
		decoder, err := zstdcodec.NewStreamDecoder(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("open zstd: %w", err)
		}
		return decoder, decoder.Close, nil
	}
	return nil, nil, fmt.Errorf("unsupported archive format %q", format)
}

// checkXZDictionaries walks every stream's index from the end of the file to
// each block header and refuses an LZMA2 dictionary over MaxXZDictionary.
func checkXZDictionaries(src io.ReaderAt, size int64) error {
	read := func(off, n int64) ([]byte, error) {
		if off < 0 || n < 0 || off+n > size {
			return nil, errXZLayout
		}
		buf := make([]byte, n)
		_, err := src.ReadAt(buf, off)
		return buf, err
	}
	end := size
	for end > 0 {
		pad, err := read(end-4, 4)
		if err != nil {
			return err
		}
		if bytes.Equal(pad, []byte{0, 0, 0, 0}) {
			end -= 4
			continue
		}
		footer, err := read(end-12, 12)
		if err != nil || !bytes.Equal(footer[10:], []byte("YZ")) {
			return errXZLayout
		}
		indexSize := (int64(binary.LittleEndian.Uint32(footer[4:8])) + 1) * 4
		indexStart := end - 12 - indexSize
		index, err := read(indexStart, indexSize)
		if err != nil {
			return err
		}
		blocks, err := xzIndexBlocks(index)
		if err != nil {
			return err
		}
		var total int64
		for _, block := range blocks {
			total += block
		}
		start := indexStart - total - 12
		offset := start + 12
		for _, block := range blocks {
			if err := checkXZBlockHeader(read, offset); err != nil {
				return err
			}
			offset += block
		}
		end = start
	}
	return nil
}

var errXZLayout = errors.New("open xz: malformed stream layout")

// xzIndexBlocks reads each block's padded size from a stream index.
func xzIndexBlocks(index []byte) ([]int64, error) {
	r := bytes.NewReader(index)
	if indicator, err := r.ReadByte(); err != nil || indicator != 0 {
		return nil, errXZLayout
	}
	count, err := xzVarint(r)
	if err != nil || count > uint64(len(index)) {
		return nil, errXZLayout
	}
	blocks := make([]int64, 0, count)
	for range count {
		unpadded, err := xzVarint(r)
		if err != nil {
			return nil, errXZLayout
		}
		if _, err := xzVarint(r); err != nil {
			return nil, errXZLayout
		}
		if unpadded == 0 || unpadded > 1<<62 {
			return nil, errXZLayout
		}
		blocks = append(blocks, int64(unpadded+3)&^3)
	}
	return blocks, nil
}

// checkXZBlockHeader reads one block header's filter flags.
func checkXZBlockHeader(read func(off, n int64) ([]byte, error), offset int64) error {
	first, err := read(offset, 1)
	if err != nil || first[0] == 0 {
		return errXZLayout
	}
	header, err := read(offset, (int64(first[0])+1)*4)
	if err != nil {
		return err
	}
	r := bytes.NewReader(header[2:])
	flags := header[1]
	for _, present := range []bool{flags&0x40 != 0, flags&0x80 != 0} {
		if present {
			if _, err := xzVarint(r); err != nil {
				return errXZLayout
			}
		}
	}
	for range int(flags&3) + 1 {
		id, err := xzVarint(r)
		if err != nil {
			return errXZLayout
		}
		propsSize, err := xzVarint(r)
		if err != nil || propsSize > uint64(r.Len()) {
			return errXZLayout
		}
		props := make([]byte, propsSize)
		_, _ = r.Read(props)
		if id != 0x21 {
			continue
		}
		if len(props) != 1 || props[0] > 40 {
			return errXZLayout
		}
		dictionary := int64(1<<32 - 1)
		if bits := props[0]; bits < 40 {
			dictionary = int64(2|bits&1) << (bits/2 + 11)
		}
		if dictionary > MaxXZDictionary {
			return fmt.Errorf("open xz: a block declares a %d-byte dictionary; at most %d bytes are decoded", dictionary, MaxXZDictionary)
		}
	}
	return nil
}

// xzVarint reads the xz multibyte integer: seven bits per byte, low first.
func xzVarint(r io.ByteReader) (uint64, error) {
	var value uint64
	for i := range 9 {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return value, nil
		}
	}
	return 0, errXZLayout
}
