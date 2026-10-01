package textfile

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClassifyFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, encoding, detected string
		data                     []byte
		class                    Class
	}{
		{"utf-8", UTF8, "", []byte("hello\n世界\n"), Supported},
		{"utf-8-bom", UTF8BOM, "", append(append([]byte{}, bomUTF8...), []byte("hello\n")...), Supported},
		{"utf-16le-bom", UTF16LEBOM, "", append(append([]byte{}, bomUTF16LE...), []byte{'h', 0, 'i', 0}...), Supported},
		{"utf-16be-bom", UTF16BEBOM, "", append(append([]byte{}, bomUTF16BE...), []byte{0, 'h', 0, 'i'}...), Supported},
		{"utf-16le-bomless-is-binary", "", "", []byte{'h', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0}, Binary},
		{"utf-16be-bomless-is-binary", "", "", []byte{0, 'h', 0, 'e', 0, 'l', 0, 'l', 0, 'o'}, Binary},
		{"cp1252", "", Unknown, []byte("caf\xe9\n"), Unsupported},
		{"elf-binary", "", "", []byte{0x7f, 'E', 'L', 'F', 0, 1, 1, 0}, Binary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Classify(tc.data)
			if got.Class != tc.class || got.Encoding != tc.encoding || got.Detected != tc.detected {
				t.Fatalf("Classify(%x) = %#v", tc.data, got)
			}
		})
	}
}

func TestDocumentRoundTripsEverySupportedEncoding(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LEBOM, UTF16BEBOM} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			before := testutil.EncodeTextFixture(t, "line one\n世界\n", encoding)
			doc, detection, err := Open(before, LimitsForRaw(int64(len(before))))
			if err != nil || detection.Encoding != encoding || doc.Encoding() != encoding {
				t.Fatalf("open = %#v detection=%#v err=%v", doc, detection, err)
			}
			after, err := doc.Encode(doc.Text())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("round trip %s drifted: before=%x after=%x err=%v", encoding, before, after, err)
			}
		})
	}
}

func TestOpenAsUTF16WithoutBOMRequiresExplicitChoice(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{UTF16LE, UTF16BE} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			before := testutil.EncodeTextFixture(t, "line one\n世界\n", encoding)
			if _, _, err := Open(before, LimitsForRaw(int64(len(before)))); err == nil {
				t.Fatal("automatic open unexpectedly accepted BOM-less UTF-16")
			}
			doc, detection, err := OpenAsUTF16WithoutBOM(before, encoding, LimitsForRaw(int64(len(before))))
			if err != nil || detection.Encoding != encoding || doc.Encoding() != encoding {
				t.Fatalf("explicit open = %#v detection=%#v err=%v", doc, detection, err)
			}
			after, err := doc.Encode(doc.Text())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("round trip %s drifted: before=%x after=%x err=%v", encoding, before, after, err)
			}
		})
	}
	if _, _, err := OpenAsUTF16WithoutBOM([]byte("text"), UTF8, LimitsForRaw(1024)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported explicit encoding error = %v, want ErrUnsupported", err)
	}
}

func TestDecodeRejectsMalformedUTF16(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		content  []byte
		encoding string
	}{
		{[]byte{0x00}, UTF16BE},
		{[]byte{0x00}, UTF16LE},
		{[]byte{0xD8, 0x00}, UTF16BE},
		{[]byte{0x00, 0xD8}, UTF16LE},
		{[]byte{0xDC, 0x00}, UTF16BE},
		{[]byte{0x00, 0xDC}, UTF16LE},
		{[]byte{0x00, 'x'}, UTF16BEBOM},
		{[]byte{'x', 0x00}, UTF16LEBOM},
		{append(append([]byte{}, bomUTF16BE...), 0xD8, 0x00), UTF16BEBOM},
		{append(append([]byte{}, bomUTF16LE...), 0x00, 0xD8), UTF16LEBOM},
	} {
		if _, ok := decode(tc.content, tc.encoding); ok {
			t.Fatalf("decode(%x, %s) unexpectedly succeeded", tc.content, tc.encoding)
		}
	}
}

func TestOpenRequiresAndEnforcesBothBounds(t *testing.T) {
	t.Parallel()
	if _, detection, err := Open([]byte("ok"), Limits{}); !errors.Is(err, ErrLimitsInvalid) {
		t.Fatalf("zero limits error = %v, want ErrLimitsInvalid", err)
	} else if detection.Class == Supported {
		t.Fatal("invalid open returned supported zero-value detection")
	}
	if _, _, err := Open([]byte("abc"), Limits{MaxRawBytes: 2, MaxTextBytes: 4}); !errors.Is(err, ErrRawTooLarge) {
		t.Fatalf("raw bound error = %v, want ErrRawTooLarge", err)
	}
	raw, ok := encodeUnchecked("世界", UTF16BEBOM)
	if !ok {
		t.Fatal("encode UTF-16 fixture")
	}
	if _, _, err := Open(raw, Limits{MaxRawBytes: int64(len(raw)), MaxTextBytes: 5}); !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("decoded bound error = %v, want ErrTextTooLarge", err)
	}
	doc, _, err := Open([]byte("four"), Limits{MaxRawBytes: 4, MaxTextBytes: 4})
	if err != nil || doc.Text() != "four" {
		t.Fatalf("exact bounds rejected: doc=%#v err=%v", doc, err)
	}
}

func TestOpenRejectsNULAcrossEveryEncoding(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LEBOM, UTF16BEBOM} {
		raw, ok := encodeUnchecked("before\x00after", encoding)
		if !ok {
			t.Fatalf("encode %s NUL fixture", encoding)
		}
		_, detection, err := Open(raw, LimitsForRaw(int64(len(raw))))
		if !errors.Is(err, ErrBinary) || detection.Class != Binary {
			t.Errorf("Open(%s, NUL) detection=%#v error=%v", encoding, detection, err)
		}
	}
}

func TestOpenAllowsControlsAcrossEveryEncoding(t *testing.T) {
	t.Parallel()
	content := "one\ttwo\r\nthree\a\b\v\f\x1b\x7f\u0085page two\n"
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LEBOM, UTF16BEBOM} {
		raw := testutil.EncodeTextFixture(t, content, encoding)
		doc, _, err := Open(raw, LimitsForRaw(1024))
		if err != nil {
			t.Fatalf("Open %s controls: %v", encoding, err)
		}
		if doc.Text() != content {
			t.Fatalf("%s Text = %q, want %q", encoding, doc.Text(), content)
		}
		after, err := doc.Encode(doc.Text())
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatalf("%s controls drifted: before=%x after=%x err=%v", encoding, raw, after, err)
		}
	}
}

func TestRoundTripDoesNotNormalizeUnicode(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("ascii-prefix-", 8) + "\u212B|A\u030A|\uFB01|\U0001F43A\n"
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LEBOM, UTF16BEBOM} {
		raw := testutil.EncodeTextFixture(t, content, encoding)
		doc, _, err := Open(raw, LimitsForRaw(1024))
		if err != nil || doc.Text() != content {
			t.Fatalf("%s normalized content: got=%q err=%v", encoding, doc.Text(), err)
		}
		after, err := doc.Encode(doc.Text())
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatalf("%s changed exact representation: before=%x after=%x err=%v", encoding, raw, after, err)
		}
	}
}

func TestDocumentIsSealedAndRawBytesAreDefensive(t *testing.T) {
	t.Parallel()
	var zero UntrustedDocument
	if _, err := zero.Encode("content"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("zero document error = %v, want ErrUnsupported", err)
	}
	raw := []byte("safe\n")
	doc, _, err := Open(raw, LimitsForRaw(1024))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	copyOne := doc.RawBytes()
	copyOne[0] = 'X'
	raw[0] = 'Y'
	copyTwo := doc.RawBytes()
	if string(copyTwo) != "safe\n" || doc.RawSHA256() != SHA256([]byte("safe\n")) {
		t.Fatalf("document backing bytes changed: raw=%q sha=%s", copyTwo, doc.RawSHA256())
	}
}

func TestEncodeBoundedRejectsNULOrOversizeText(t *testing.T) {
	t.Parallel()
	limits := Limits{MaxRawBytes: 4, MaxTextBytes: 4}
	if _, err := EncodeBounded("a\x00b", UTF8, limits); !errors.Is(err, ErrBinary) {
		t.Fatalf("NUL error = %v, want ErrBinary", err)
	}
	if _, err := EncodeBounded("hello", UTF8, limits); !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("text bound error = %v, want ErrTextTooLarge", err)
	}
	if _, err := EncodeBounded("éé", UTF16BEBOM, limits); !errors.Is(err, ErrRawTooLarge) {
		t.Fatalf("raw bound error = %v, want ErrRawTooLarge", err)
	}
}

func TestEncodeBoundedRequiresExplicitSupportedEncoding(t *testing.T) {
	t.Parallel()
	if _, err := EncodeBounded("content", "", LimitsForRaw(1024)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("empty encoding error = %v, want ErrUnsupported", err)
	}
}

func TestTrimIncompleteTailCompletesCappedPrefixForEveryEncoding(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LEBOM, UTF16BEBOM} {
		raw := testutil.EncodeTextFixture(t, "line\n\U0001F43A", encoding)
		if got := TrimIncompleteTail(raw); !bytes.Equal(got, raw) {
			t.Fatalf("complete %s changed: got=%x want=%x", encoding, got, raw)
		}
		trimmed := TrimIncompleteTail(raw[:len(raw)-1])
		doc, _, err := Open(trimmed, LimitsForRaw(int64(len(raw))))
		if err != nil || doc.Text() != "line\n" {
			t.Fatalf("trimmed %s = %x text=%q err=%v", encoding, trimmed, doc.Text(), err)
		}
	}
}

func TestEncodeBoundedSucceedsAtExactBoundsForEveryEncoding(t *testing.T) {
	t.Parallel()
	content := "A\U0001F43A\n"
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LE, UTF16LEBOM, UTF16BE, UTF16BEBOM} {
		want := testutil.EncodeTextFixture(t, content, encoding)
		got, err := EncodeBounded(content, encoding, Limits{
			MaxRawBytes: int64(len(want)), MaxTextBytes: int64(len(content)),
		})
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("EncodeBounded %s = %x err=%v, want %x", encoding, got, err, want)
		}
	}
}

func TestDocumentEncodeRetainsOpenBounds(t *testing.T) {
	t.Parallel()
	doc, _, err := Open([]byte("four"), Limits{MaxRawBytes: 4, MaxTextBytes: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := doc.Encode("five!"); !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("oversize Document.Encode error = %v, want ErrTextTooLarge", err)
	}
	if _, err := doc.Encode("a\x00b"); !errors.Is(err, ErrBinary) {
		t.Fatalf("NUL Document.Encode error = %v, want ErrBinary", err)
	}
	got, err := doc.Encode("a\x1bb")
	if err != nil || string(got) != "a\x1bb" {
		t.Fatalf("control Document.Encode = %q err=%v", got, err)
	}
}

func TestMalformedUTF8AndBOMPayloadsFailClosed(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{
		{0xC0, 0xAF},
		{0xED, 0xA0, 0x80},
		append(append([]byte{}, bomUTF8...), 0xC0, 0xAF),
		append(append([]byte{}, bomUTF16BE...), 0xD8, 0x00),
		append(append([]byte{}, bomUTF16LE...), 0x00, 0xD8),
	} {
		if doc, _, err := Open(raw, LimitsForRaw(1024)); err == nil || doc.valid {
			t.Fatalf("Open malformed %x = %#v err=%v", raw, doc, err)
		}
	}
}

func FuzzEncodeDecodeRoundTrip(f *testing.F) {
	for _, seed := range []string{"plain", "line one\n世界\n", "\u202Evisible"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
			t.Skip()
		}
		for _, encoding := range []string{UTF8, UTF8BOM, UTF16LE, UTF16LEBOM, UTF16BE, UTF16BEBOM} {
			raw, err := testutil.ReferenceEncodeText(content, encoding)
			testutil.FailErr(t, "reference encode fuzz text", err)
			decoded, ok := decode(raw, encoding)
			if !ok || decoded != content {
				t.Fatalf("round trip %s: decoded=%q ok=%v", encoding, decoded, ok)
			}
			got, err := EncodeBounded(content, encoding, Limits{
				MaxRawBytes: max(1, int64(len(raw))), MaxTextBytes: max(1, int64(len(content))),
			})
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("bounded encode %s = %x err=%v, want %x", encoding, got, err, raw)
			}
		}
	})
}

func FuzzOpenNeverPanics(f *testing.F) {
	f.Add([]byte("text\n"), int64(16), int64(32))
	f.Add([]byte{0xfe, 0xff, 0, 'x'}, int64(4), int64(8))
	f.Fuzz(func(t *testing.T, raw []byte, maxRaw, maxText int64) {
		_, _, _ = Open(raw, Limits{MaxRawBytes: maxRaw, MaxTextBytes: maxText})
	})
}

func FuzzOpenEncodePreservesAcceptedRepresentation(f *testing.F) {
	f.Add([]byte("text\n"))
	f.Add(append(append([]byte{}, bomUTF8...), []byte("text\n")...))
	f.Add(append(append([]byte{}, bomUTF16BE...), 0, 'x', 0, '\n'))
	f.Fuzz(func(t *testing.T, raw []byte) {
		max := int64(len(raw))
		if max == 0 {
			max = 1
		}
		doc, _, err := Open(raw, LimitsForRaw(max))
		if err != nil {
			return
		}
		after, err := doc.Encode(doc.Text())
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatalf("accepted payload drifted: before=%x after=%x encoding=%s err=%v", raw, after, doc.Encoding(), err)
		}
	})
}
