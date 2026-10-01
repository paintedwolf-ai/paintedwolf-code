package gotreesitter

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

var languageBlobEnvelopeMagic = [8]byte{'G', 'T', 'S', 'B', 'L', 'O', 'B', 0}

const (
	languageBlobEnvelopeVersionOffset              = len(languageBlobEnvelopeMagic)
	languageBlobEnvelopeFlagsOffset                = languageBlobEnvelopeVersionOffset + 2
	languageBlobEnvelopePayloadLengthOffset        = languageBlobEnvelopeFlagsOffset + 2
	languageBlobEnvelopeHeaderSize                 = languageBlobEnvelopePayloadLengthOffset + 8
	languageBlobEnvelopeVersion                    = uint16(1)
	languageBlobEnvelopeFlagLargeStateGotosTrailer = uint16(1)
)

// WrapLanguageBlobEnvelope wraps a gzip-compressed Language blob whose
// decompressed stream contains a LargeStateGotos trailer. The outer magic is
// deliberately not a gzip header: runtimes predating trailer support therefore
// reject the blob at gzip.NewReader instead of successfully decoding the gob
// payload with LargeStateGotos missing.
//
// The envelope is versioned and length-delimited so current runtimes also fail
// closed on unknown versions, flags, truncation, and trailing bytes. Callers
// must only wrap blobs that actually contain a non-empty trailer.
func WrapLanguageBlobEnvelope(compressed []byte) ([]byte, error) {
	if !hasGzipHeader(compressed) {
		return nil, fmt.Errorf("wrap language blob envelope: payload is not gzip data")
	}

	out := make([]byte, languageBlobEnvelopeHeaderSize+len(compressed))
	copy(out, languageBlobEnvelopeMagic[:])
	binary.BigEndian.PutUint16(out[languageBlobEnvelopeVersionOffset:languageBlobEnvelopeFlagsOffset], languageBlobEnvelopeVersion)
	binary.BigEndian.PutUint16(out[languageBlobEnvelopeFlagsOffset:languageBlobEnvelopePayloadLengthOffset], languageBlobEnvelopeFlagLargeStateGotosTrailer)
	binary.BigEndian.PutUint64(out[languageBlobEnvelopePayloadLengthOffset:languageBlobEnvelopeHeaderSize], uint64(len(compressed)))
	copy(out[languageBlobEnvelopeHeaderSize:], compressed)
	return out, nil
}

// UnwrapLanguageBlobEnvelope returns the gzip payload and whether its envelope
// requires a non-empty LargeStateGotos trailer. Legacy gzip blobs are returned
// unchanged with expectsTrailer=false, preserving the original wire format.
func UnwrapLanguageBlobEnvelope(data []byte) (compressed []byte, expectsTrailer bool, err error) {
	if !bytes.HasPrefix(data, languageBlobEnvelopeMagic[:]) {
		return data, false, nil
	}
	if len(data) < languageBlobEnvelopeHeaderSize {
		return nil, false, fmt.Errorf("unwrap language blob envelope: truncated header: got %d bytes, need %d", len(data), languageBlobEnvelopeHeaderSize)
	}

	version := binary.BigEndian.Uint16(data[languageBlobEnvelopeVersionOffset:languageBlobEnvelopeFlagsOffset])
	if version != languageBlobEnvelopeVersion {
		return nil, false, fmt.Errorf("unwrap language blob envelope: unsupported version %d", version)
	}
	flags := binary.BigEndian.Uint16(data[languageBlobEnvelopeFlagsOffset:languageBlobEnvelopePayloadLengthOffset])
	if flags != languageBlobEnvelopeFlagLargeStateGotosTrailer {
		return nil, false, fmt.Errorf("unwrap language blob envelope: unsupported flags %#x", flags)
	}

	wantLen := binary.BigEndian.Uint64(data[languageBlobEnvelopePayloadLengthOffset:languageBlobEnvelopeHeaderSize])
	gotLen := uint64(len(data) - languageBlobEnvelopeHeaderSize)
	if wantLen != gotLen {
		return nil, false, fmt.Errorf("unwrap language blob envelope: payload length = %d, actual = %d", wantLen, gotLen)
	}
	payload := data[languageBlobEnvelopeHeaderSize:]
	if !hasGzipHeader(payload) {
		return nil, false, fmt.Errorf("unwrap language blob envelope: payload is not gzip data")
	}
	return payload, true, nil
}

func hasGzipHeader(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}
