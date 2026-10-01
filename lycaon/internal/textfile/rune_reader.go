package textfile

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// RuneReader decodes a declared encoding without retaining the complete file.
// Malformed input fails instead of replacing bytes with replacement characters.
type RuneReader struct {
	input *bufio.Reader
	order binary.ByteOrder
}

func NewRuneReader(input io.Reader, encoding string) (*RuneReader, error) {
	r := &RuneReader{input: bufio.NewReaderSize(input, 64<<10)}
	var bom []byte
	switch encoding {
	case UTF8:
	case UTF8BOM:
		bom = bomUTF8
	case UTF16LE, UTF16LEBOM:
		r.order = binary.LittleEndian
		if encoding == UTF16LEBOM {
			bom = bomUTF16LE
		}
	case UTF16BE, UTF16BEBOM:
		r.order = binary.BigEndian
		if encoding == UTF16BEBOM {
			bom = bomUTF16BE
		}
	default:
		return nil, ErrUnsupported
	}
	if len(bom) > 0 {
		prefix := make([]byte, len(bom))
		if _, err := io.ReadFull(r.input, prefix); err != nil || !bytes.Equal(prefix, bom) {
			return nil, ErrUnsupported
		}
	}
	return r, nil
}

func (r *RuneReader) ReadRune() (rune, int, error) {
	if r.order == nil {
		value, size, err := r.input.ReadRune()
		if err == nil && value == 0 {
			return 0, 0, ErrBinary
		}
		if value == utf8.RuneError && size == 1 {
			return 0, 0, ErrUnsupported
		}
		return value, size, err
	}
	first, err := r.unit()
	if err != nil {
		return 0, 0, err
	}
	if first == 0 {
		return 0, 0, ErrBinary
	}
	if first < 0xd800 || first > 0xdfff {
		return rune(first), 2, nil
	}
	if first > 0xdbff {
		return 0, 0, ErrUnsupported
	}
	second, err := r.unit()
	if err != nil || second < 0xdc00 || second > 0xdfff {
		return 0, 0, ErrUnsupported
	}
	return utf16.DecodeRune(rune(first), rune(second)), 4, nil
}

func (r *RuneReader) unit() (uint16, error) {
	var raw [2]byte
	_, err := io.ReadFull(r.input, raw[:])
	if err == io.ErrUnexpectedEOF {
		err = ErrUnsupported
	}
	return r.order.Uint16(raw[:]), err
}
