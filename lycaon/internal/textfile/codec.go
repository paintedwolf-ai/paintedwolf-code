// Package textfile preserves on-disk encoding while exposing UTF-8 text.
package textfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Supported on-disk encodings. New files use UTF8; existing files retain the
// encoding discovered when their UntrustedDocument was opened.
const (
	UTF8       = "utf-8"
	UTF8BOM    = "utf-8-bom"
	UTF16LE    = "utf-16le"
	UTF16LEBOM = "utf-16le-bom"
	UTF16BE    = "utf-16be"
	UTF16BEBOM = "utf-16be-bom"
	Unknown    = "unknown"
)

var (
	bomUTF8    = []byte{0xef, 0xbb, 0xbf}
	bomUTF16LE = []byte{0xff, 0xfe}
	bomUTF16BE = []byte{0xfe, 0xff}
)

// Class is the closed outcome of Classify.
type Class int

const (
	invalid Class = iota
	Supported
	Unsupported
	Binary
)

// Detection describes the encoding classification for a byte sample.
type Detection struct {
	Class    Class
	Encoding string
	Detected string
}

// Limits are mandatory resource bounds for Open. MaxTextBytes includes any
// UTF-16-to-UTF-8 expansion after decoding.
type Limits struct {
	MaxRawBytes  int64
	MaxTextBytes int64
}

// LimitsForRaw returns conservative decoding limits for one caller-supplied raw
// byte ceiling. UTF-16 can expand when represented as UTF-8, so decoded text is
// allowed up to twice the raw bound.
func LimitsForRaw(maxRawBytes int64) Limits {
	maxTextBytes := maxRawBytes
	if maxRawBytes > 0 && maxRawBytes <= math.MaxInt64/2 {
		maxTextBytes = maxRawBytes * 2
	}
	return Limits{MaxRawBytes: maxRawBytes, MaxTextBytes: maxTextBytes}
}

var (
	ErrLimitsInvalid = errors.New("text file limits invalid")
	ErrRawTooLarge   = errors.New("text file raw payload too large")
	ErrTextTooLarge  = errors.New("text file decoded payload too large")
	ErrUnsupported   = errors.New("text file encoding unsupported")
	ErrBinary        = errors.New("text file binary payload")
)

// UntrustedDocument is an immutable, lossless text-file snapshot.
type UntrustedDocument struct {
	text         string
	encoding     string
	raw          []byte
	rawSHA256    string
	maxRawBytes  int64
	maxTextBytes int64
	valid        bool
}

// Classify detects encodings that can be round-tripped without guessing.
func Classify(data []byte) Detection {
	if len(data) >= 3 && bytes.HasPrefix(data, bomUTF8) {
		return Detection{Class: Supported, Encoding: UTF8BOM}
	}
	if len(data) >= 2 && bytes.HasPrefix(data, bomUTF16LE) {
		return Detection{Class: Supported, Encoding: UTF16LEBOM}
	}
	if len(data) >= 2 && bytes.HasPrefix(data, bomUTF16BE) {
		return Detection{Class: Supported, Encoding: UTF16BEBOM}
	}

	if bytes.IndexByte(data, 0) >= 0 {
		return Detection{Class: Binary}
	}
	if utf8.Valid(data) {
		return Detection{Class: Supported, Encoding: UTF8}
	}
	return Detection{Class: Unsupported, Detected: Unknown}
}

// OpenAsUTF16WithoutBOM decodes an explicitly selected byte order.
func OpenAsUTF16WithoutBOM(raw []byte, encoding string, limits Limits) (UntrustedDocument, Detection, error) {
	if encoding != UTF16LE && encoding != UTF16BE {
		return UntrustedDocument{}, Detection{}, ErrUnsupported
	}
	if bytes.HasPrefix(raw, bomUTF16LE) || bytes.HasPrefix(raw, bomUTF16BE) {
		return UntrustedDocument{}, Detection{Class: Unsupported, Detected: Unknown}, ErrUnsupported
	}
	return openDetected(raw, Detection{Class: Supported, Encoding: encoding}, limits)
}

// Open validates and decodes one bounded text payload.
func Open(raw []byte, limits Limits) (UntrustedDocument, Detection, error) {
	return openDetected(raw, Classify(raw), limits)
}

func openDetected(raw []byte, detection Detection, limits Limits) (UntrustedDocument, Detection, error) {
	text, detection, err := decodeDetected(raw, detection, limits)
	if err != nil {
		return UntrustedDocument{}, detection, err
	}
	return UntrustedDocument{
		text: text, encoding: detection.Encoding,
		raw: append([]byte(nil), raw...), rawSHA256: SHA256(raw),
		maxRawBytes: limits.MaxRawBytes, maxTextBytes: limits.MaxTextBytes, valid: true,
	}, detection, nil
}

// Decode validates bounded untrusted text without retaining a lossless snapshot.
func Decode(raw []byte, limits Limits) (string, Detection, error) {
	return decodeDetected(raw, Classify(raw), limits)
}

func decodeDetected(raw []byte, detection Detection, limits Limits) (string, Detection, error) {
	if limits.MaxRawBytes <= 0 || limits.MaxTextBytes <= 0 {
		return "", Detection{}, ErrLimitsInvalid
	}
	if int64(len(raw)) > limits.MaxRawBytes {
		return "", Detection{}, ErrRawTooLarge
	}
	if detection.Class != Supported {
		if detection.Class == Binary {
			return "", detection, ErrBinary
		}
		return "", detection, ErrUnsupported
	}
	text, ok := decode(raw, detection.Encoding)
	if !ok {
		detection = Detection{Class: Unsupported, Detected: Unknown}
		return "", detection, ErrUnsupported
	}
	if int64(len(text)) > limits.MaxTextBytes {
		return "", detection, ErrTextTooLarge
	}
	if strings.IndexByte(text, 0) >= 0 {
		detection = Detection{Class: Binary}
		return "", detection, ErrBinary
	}
	return text, detection, nil
}

// Text returns structurally validated but still untrusted UTF-8 text.
func (d UntrustedDocument) Text() string { return d.text }

// Encoding returns the exact supported on-disk representation.
func (d UntrustedDocument) Encoding() string { return d.encoding }

// RawBytes returns a copy of the original payload.
func (d UntrustedDocument) RawBytes() []byte { return append([]byte(nil), d.raw...) }

// RawSHA256 identifies the exact original payload.
func (d UntrustedDocument) RawSHA256() string { return d.rawSHA256 }

// Encode returns bytes in this valid document's original encoding and retained
// resource bounds.
func (d UntrustedDocument) Encode(text string) ([]byte, error) {
	if !d.valid {
		return nil, ErrUnsupported
	}
	return EncodeBounded(text, d.encoding, Limits{
		MaxRawBytes: d.maxRawBytes, MaxTextBytes: d.maxTextBytes,
	})
}

// SHA256 is the stable identity of the exact on-disk bytes.
func SHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// TrimIncompleteTail removes a partial trailing codepoint from a byte-capped prefix.
// Complete payloads are validated unchanged by Open.
func TrimIncompleteTail(raw []byte) []byte {
	detection := Classify(raw)
	switch detection.Encoding {
	case UTF16LE, UTF16LEBOM:
		return trimIncompleteUTF16Tail(raw, false, detection.Encoding == UTF16LEBOM)
	case UTF16BE, UTF16BEBOM:
		return trimIncompleteUTF16Tail(raw, true, detection.Encoding == UTF16BEBOM)
	default:
		return trimIncompleteUTF8Tail(raw)
	}
}

func trimIncompleteUTF8Tail(raw []byte) []byte {
	for removed := 0; removed < utf8.UTFMax-1 && len(raw) > 0; removed++ {
		if r, size := utf8.DecodeLastRune(raw); r != utf8.RuneError || size > 1 {
			break
		}
		raw = raw[:len(raw)-1]
	}
	return raw
}

func trimIncompleteUTF16Tail(raw []byte, bigEndian, bom bool) []byte {
	offset := 0
	if bom {
		offset = 2
	}
	if len(raw) < offset {
		return raw
	}
	if (len(raw)-offset)%2 != 0 {
		raw = raw[:len(raw)-1]
	}
	if len(raw)-offset < 2 {
		return raw
	}
	i := len(raw) - 2
	unit := uint16(raw[i])<<8 | uint16(raw[i+1])
	if !bigEndian {
		unit = uint16(raw[i+1])<<8 | uint16(raw[i])
	}
	if 0xD800 <= unit && unit <= 0xDBFF {
		return raw[:i]
	}
	return raw
}

func decode(content []byte, encoding string) (string, bool) {
	switch encoding {
	case UTF8:
		if !utf8.Valid(content) {
			return "", false
		}
		return string(content), true
	case UTF8BOM:
		if !bytes.HasPrefix(content, bomUTF8) || !utf8.Valid(content) {
			return "", false
		}
		return strings.TrimPrefix(string(content), "\xef\xbb\xbf"), true
	case UTF16LE:
		return decodeUTF16(content, false, false)
	case UTF16LEBOM:
		return decodeUTF16(content, false, true)
	case UTF16BE:
		return decodeUTF16(content, true, false)
	case UTF16BEBOM:
		return decodeUTF16(content, true, true)
	default:
		return "", false
	}
}

// EncodeBounded encodes valid text within explicit resource bounds.
func EncodeBounded(content, encoding string, limits Limits) ([]byte, error) {
	if limits.MaxRawBytes <= 0 || limits.MaxTextBytes <= 0 {
		return nil, ErrLimitsInvalid
	}
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return nil, ErrBinary
	}
	if int64(len(content)) > limits.MaxTextBytes {
		return nil, ErrTextTooLarge
	}
	encoded, ok := encodeUnchecked(content, encoding)
	if !ok {
		return nil, ErrUnsupported
	}
	if int64(len(encoded)) > limits.MaxRawBytes {
		return nil, ErrRawTooLarge
	}
	return encoded, nil
}

func encodeUnchecked(content, encoding string) ([]byte, bool) {
	switch encoding {
	case UTF8:
		return []byte(content), true
	case UTF8BOM:
		return append(append([]byte{}, bomUTF8...), []byte(content)...), true
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

func decodeUTF16(content []byte, bigEndian, bom bool) (string, bool) {
	if bom {
		prefix := bomUTF16LE
		if bigEndian {
			prefix = bomUTF16BE
		}
		if !bytes.HasPrefix(content, prefix) {
			return "", false
		}
		content = content[len(prefix):]
	}
	if len(content)%2 != 0 {
		return "", false
	}
	units := make([]uint16, 0, len(content)/2)
	for i := 0; i < len(content); i += 2 {
		unit := uint16(content[i])<<8 | uint16(content[i+1])
		if !bigEndian {
			unit = uint16(content[i+1])<<8 | uint16(content[i])
		}
		units = append(units, unit)
	}
	var out strings.Builder
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case 0xD800 <= u && u <= 0xDBFF:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", false
			}
			out.WriteRune(utf16.DecodeRune(rune(u), rune(units[i+1])))
			i++
		case 0xDC00 <= u && u <= 0xDFFF:
			return "", false
		default:
			out.WriteRune(rune(u))
		}
	}
	return out.String(), true
}

func encodeUTF16(content string, bigEndian, bom bool) []byte {
	units := utf16.Encode([]rune(content))
	capacity := len(units) * 2
	if bom {
		capacity += 2
	}
	out := make([]byte, 0, capacity)
	if bom {
		if bigEndian {
			out = append(out, bomUTF16BE...)
		} else {
			out = append(out, bomUTF16LE...)
		}
	}
	for _, unit := range units {
		if bigEndian {
			out = append(out, byte(unit>>8), byte(unit&0x00FF))
		} else {
			out = append(out, byte(unit&0x00FF), byte(unit>>8))
		}
	}
	return out
}
