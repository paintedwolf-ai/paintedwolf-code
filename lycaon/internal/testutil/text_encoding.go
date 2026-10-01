package testutil

import (
	"encoding/binary"
	"fmt"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// SelfIdentifyingTextEncodings returns representations that implicit readers
// can classify without a human-selected decode mode.
func SelfIdentifyingTextEncodings() []string {
	return []string{"utf-8", "utf-8-bom", "utf-16le-bom", "utf-16be-bom"}
}

// EncodeTextFixture is the concise test-facing form of the independent oracle.
func EncodeTextFixture(t *testing.T, text, encoding string) []byte {
	t.Helper()
	raw, err := ReferenceEncodeText(text, encoding)
	FailErr(t, "reference encode text fixture", err)
	return raw
}

// ReferenceEncodeText produces test fixtures without calling the production
// text gateway. Keeping this oracle independent catches matching encode/decode
// mistakes in the gateway itself.
func ReferenceEncodeText(text, encoding string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("reference encoding %q: invalid UTF-8 text", encoding)
	}
	switch encoding {
	case "utf-8":
		return []byte(text), nil
	case "utf-8-bom":
		return append([]byte{0xef, 0xbb, 0xbf}, []byte(text)...), nil
	case "utf-16le", "utf-16le-bom", "utf-16be", "utf-16be-bom":
		units := utf16.Encode([]rune(text))
		bom := encoding == "utf-16le-bom" || encoding == "utf-16be-bom"
		bigEndian := encoding == "utf-16be" || encoding == "utf-16be-bom"
		raw := make([]byte, 0, len(units)*2+2)
		if bom {
			if bigEndian {
				raw = append(raw, 0xfe, 0xff)
			} else {
				raw = append(raw, 0xff, 0xfe)
			}
		}
		var order binary.ByteOrder = binary.LittleEndian
		if bigEndian {
			order = binary.BigEndian
		}
		for _, unit := range units {
			var pair [2]byte
			order.PutUint16(pair[:], unit)
			raw = append(raw, pair[:]...)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("reference encoding %q: unsupported", encoding)
	}
}
