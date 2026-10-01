package textfile

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGoldenCorpusMatchesIndependentEncodingOracle(t *testing.T) {
	t.Parallel()
	var corpus struct {
		Text      string            `json:"text"`
		Encodings map[string]string `json:"encodings"`
	}
	raw, err := os.ReadFile("testdata/golden.json")
	testutil.FailErr(t, "read golden encoding corpus", err)
	testutil.FailErr(t, "decode golden encoding corpus", json.Unmarshal(raw, &corpus))
	for encoding, encodedHex := range corpus.Encodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			want, err := hex.DecodeString(encodedHex)
			testutil.FailErr(t, "decode golden bytes", err)
			reference, err := testutil.ReferenceEncodeText(corpus.Text, encoding)
			testutil.FailErr(t, "reference encode golden text", err)
			if !bytes.Equal(reference, want) {
				t.Fatalf("reference bytes = %x, want %x", reference, want)
			}
			var doc UntrustedDocument
			var detection Detection
			if encoding == UTF16LE || encoding == UTF16BE {
				doc, detection, err = OpenAsUTF16WithoutBOM(want, encoding, LimitsForRaw(int64(len(want))))
			} else {
				doc, detection, err = Open(want, LimitsForRaw(int64(len(want))))
			}
			testutil.FailErr(t, "open golden bytes", err)
			encoded, err := EncodeBounded(corpus.Text, encoding, LimitsForRaw(int64(len(want))))
			testutil.FailErr(t, "encode golden text", err)
			if doc.Text() != corpus.Text || doc.Encoding() != encoding || detection.Encoding != encoding || !bytes.Equal(encoded, want) {
				t.Fatalf("golden mismatch: text=%q encoding=%q detection=%#v encoded=%x want=%x", doc.Text(), doc.Encoding(), detection, encoded, want)
			}
		})
	}
}
