package textfile

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// EncodedSize validates text and measures its exact selected representation
// without allocating the representation. Callers can admit capacity first.
func EncodedSize(content, encoding string) (int64, error) {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return 0, ErrBinary
	}
	size, ok := encodedSize(content, encoding)
	if !ok {
		return 0, ErrUnsupported
	}
	return size, nil
}

func encodedSize(content, encoding string) (int64, bool) {
	switch encoding {
	case UTF8:
		return int64(len(content)), true
	case UTF8BOM:
		return int64(len(content)) + 3, true
	case UTF16LE, UTF16BE, UTF16LEBOM, UTF16BEBOM:
		var size int64
		if encoding == UTF16LEBOM || encoding == UTF16BEBOM {
			size = 2
		}
		for _, r := range content {
			if r > 0xffff {
				size += 4
			} else {
				size += 2
			}
		}
		return size, true
	default:
		return 0, false
	}
}

// EncodeBounded rejects oversized output before allocating encoded bytes.
func EncodeBounded(content, encoding string, limits Limits) ([]byte, error) {
	if limits.MaxRawBytes <= 0 || limits.MaxTextBytes <= 0 {
		return nil, ErrLimitsInvalid
	}
	size, err := EncodedSize(content, encoding)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limits.MaxTextBytes {
		return nil, ErrTextTooLarge
	}
	if size > limits.MaxRawBytes {
		return nil, ErrRawTooLarge
	}
	encoded, _ := encodeUnchecked(content, encoding)
	return encoded, nil
}

// encodeUnchecked also creates deliberately invalid fixtures for the codec's
// classifier tests; production calls validate through EncodeBounded first.
func encodeUnchecked(content, encoding string) ([]byte, bool) {
	switch encoding {
	case UTF8:
		return []byte(content), true
	case UTF8BOM:
		out := make([]byte, len(bomUTF8)+len(content))
		copy(out, bomUTF8)
		copy(out[len(bomUTF8):], content)
		return out, true
	case UTF16LE:
		return encodeUTF16(content, false, false), true
	case UTF16LEBOM:
		return encodeUTF16(content, false, true), true
	case UTF16BE:
		return encodeUTF16(content, true, false), true
	case UTF16BEBOM:
		return encodeUTF16(content, true, true), true
	default:
		return nil, false
	}
}

// encodeUTF16 writes directly into its exact byte capacity. It retains no
// intermediate rune or code-unit array.
func encodeUTF16(content string, bigEndian, bom bool) []byte {
	encoding := UTF16LE
	if bom {
		encoding = UTF16LEBOM
	}
	size, _ := encodedSize(content, encoding)
	out := make([]byte, 0, int(size))
	if bom {
		if bigEndian {
			out = append(out, bomUTF16BE...)
		} else {
			out = append(out, bomUTF16LE...)
		}
	}
	unit := func(value uint16) {
		if bigEndian {
			out = append(out, byte(value>>8), byte(value&0xff))
		} else {
			out = append(out, byte(value&0xff), byte(value>>8))
		}
	}
	for _, r := range content {
		if r <= 0xffff {
			unit(uint16(r & 0xffff))
		} else {
			first, second := utf16.EncodeRune(r)
			unit(uint16(first & 0xffff))
			unit(uint16(second & 0xffff))
		}
	}
	return out
}
