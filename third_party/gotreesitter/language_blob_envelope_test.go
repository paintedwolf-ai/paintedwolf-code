package gotreesitter

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"testing"
)

func gzipPayloadForEnvelopeTest(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if _, err := gzw.Write(payload); err != nil {
		t.Fatalf("gzip Write: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func TestLanguageBlobEnvelopeRoundTripAndRejectsLegacyGzipReader(t *testing.T) {
	compressed := gzipPayloadForEnvelopeTest(t, []byte("gob payload plus trailer"))
	enveloped, err := WrapLanguageBlobEnvelope(compressed)
	if err != nil {
		t.Fatalf("WrapLanguageBlobEnvelope: %v", err)
	}
	if bytes.HasPrefix(enveloped, []byte{0x1f, 0x8b}) {
		t.Fatal("enveloped blob still begins with gzip magic; old runtimes would accept it")
	}
	if _, err := gzip.NewReader(bytes.NewReader(enveloped)); err == nil {
		t.Fatal("legacy gzip-first loader accepted an enveloped blob")
	}

	got, expectsTrailer, err := UnwrapLanguageBlobEnvelope(enveloped)
	if err != nil {
		t.Fatalf("UnwrapLanguageBlobEnvelope: %v", err)
	}
	if !expectsTrailer {
		t.Fatal("UnwrapLanguageBlobEnvelope did not require a trailer")
	}
	if !bytes.Equal(got, compressed) {
		t.Fatal("unwrapped gzip payload differs from input")
	}
}

func TestUnwrapLanguageBlobEnvelopeLeavesLegacyBlobUnchanged(t *testing.T) {
	legacy := gzipPayloadForEnvelopeTest(t, []byte("legacy gob payload"))
	got, expectsTrailer, err := UnwrapLanguageBlobEnvelope(legacy)
	if err != nil {
		t.Fatalf("UnwrapLanguageBlobEnvelope: %v", err)
	}
	if expectsTrailer {
		t.Fatal("legacy blob unexpectedly requires a trailer")
	}
	if !bytes.Equal(got, legacy) {
		t.Fatal("legacy blob bytes changed while unwrapping")
	}
}

func TestUnwrapLanguageBlobEnvelopeRejectsMalformedEnvelope(t *testing.T) {
	compressed := gzipPayloadForEnvelopeTest(t, []byte("payload"))
	valid, err := WrapLanguageBlobEnvelope(compressed)
	if err != nil {
		t.Fatalf("WrapLanguageBlobEnvelope: %v", err)
	}

	tests := map[string]func([]byte) []byte{
		"truncated header": func(in []byte) []byte {
			return append([]byte(nil), in[:len(languageBlobEnvelopeMagic)]...)
		},
		"unknown version": func(in []byte) []byte {
			out := append([]byte(nil), in...)
			binary.BigEndian.PutUint16(out[languageBlobEnvelopeVersionOffset:languageBlobEnvelopeFlagsOffset], languageBlobEnvelopeVersion+1)
			return out
		},
		"unknown flags": func(in []byte) []byte {
			out := append([]byte(nil), in...)
			binary.BigEndian.PutUint16(out[languageBlobEnvelopeFlagsOffset:languageBlobEnvelopePayloadLengthOffset], 0x8000)
			return out
		},
		"wrong payload length": func(in []byte) []byte {
			out := append([]byte(nil), in...)
			binary.BigEndian.PutUint64(out[languageBlobEnvelopePayloadLengthOffset:languageBlobEnvelopeHeaderSize], uint64(len(compressed)+1))
			return out
		},
		"non-gzip payload": func(in []byte) []byte {
			out := append([]byte(nil), in...)
			out[languageBlobEnvelopeHeaderSize] = 0
			return out
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := UnwrapLanguageBlobEnvelope(mutate(valid)); err == nil {
				t.Fatal("expected malformed envelope to be rejected")
			}
		})
	}
}

func TestWrapLanguageBlobEnvelopeRejectsNonGzipPayload(t *testing.T) {
	if _, err := WrapLanguageBlobEnvelope([]byte("not gzip")); err == nil {
		t.Fatal("expected non-gzip payload to be rejected")
	}
}
