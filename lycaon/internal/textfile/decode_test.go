package textfile

import (
	"errors"
	"testing"
)

func TestDecodeSharesDocumentValidation(t *testing.T) {
	for name, raw := range map[string][]byte{
		"utf8": []byte("hello π"), "utf8 bom": {0xef, 0xbb, 0xbf, 'x'},
		"utf16le": {0xff, 0xfe, 'x', 0}, "utf16be": {0xfe, 0xff, 0, 'x'},
		"binary": {'x', 0}, "invalid utf8": {0xff}, "invalid utf16": {0xff, 0xfe, 0x00, 0xd8},
	} {
		t.Run(name, func(t *testing.T) {
			limits := LimitsForRaw(64)
			text, detection, err := Decode(raw, limits)
			doc, docDetection, docErr := Open(raw, limits)
			if !errors.Is(err, docErr) || detection != docDetection || text != doc.Text() {
				t.Fatalf("decode=%q/%+v/%v open=%q/%+v/%v", text, detection, err, doc.Text(), docDetection, docErr)
			}
		})
	}
	for _, limits := range []Limits{{}, {MaxRawBytes: 1, MaxTextBytes: 100}, {MaxRawBytes: 100, MaxTextBytes: 1}} {
		_, _, err := Decode([]byte("hello"), limits)
		_, _, docErr := Open([]byte("hello"), limits)
		if err == nil || !errors.Is(err, docErr) {
			t.Fatalf("limits=%+v decode=%v open=%v", limits, err, docErr)
		}
	}
}
