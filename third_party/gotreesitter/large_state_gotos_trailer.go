package gotreesitter

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"slices"
)

// largeStateGotoPair is the gob-stable, order-deterministic wire form of one
// Language.LargeStateGotos entry (see language.go). It exists purely for blob
// serialization; runtime code never sees this type.
//
// Why this exists: encoding/gob's built-in map codec iterates map fields via
// reflect's Value.MapRange, which -- like a plain `range` over a Go map --
// starts from a fresh random bucket/slot offset on every call (see
// internal/runtime/maps.(*Iter).Init in the Go runtime, which calls rand()
// each time it is invoked). That makes gob's output for any map field vary
// from run to run even when the map's content is completely unchanged.
// Language.LargeStateGotos is the only *exported* map field on Language (the
// name-lookup maps are unexported and gob never sees them), so it is the
// only field capable of making two encodes of an unchanged Language produce
// different blob bytes.
//
// gob has no supported hook to control a map field's iteration order.
// Implementing GobEncoder/GobDecoder on the field's type looks tempting but
// does not work here: it changes gob's wire *type* for that field from a
// native "map" CommonType to an opaque encoded-bytes representation, and
// encoding/gob's decoder (compileDec/compatibleType) hard-errors the moment
// a wire field's GobEncoderT marker disagrees with what the local type
// expects. Since gob transmits a struct's *type descriptor* (its full field
// list) once per encode regardless of which fields hold non-zero values,
// this would break decoding of every existing blob's LargeStateGotos field
// -- not just c_sharp's -- because the type descriptor for that field name
// would no longer match what old blobs declared.
//
// Adding a brand-new exported field to Language has a related problem: gob's
// type descriptor for Language changes to describe the new field the moment
// it exists, which changes the encoded bytes for *every* blob -- including
// the 200+ languages that never populate LargeStateGotos -- even though the
// new field's value stays at its zero value for them. (Verified empirically:
// two structs whose only difference is an added, always-nil slice field gob
// encode to different byte sequences.)
//
// So LargeStateGotos is never handed to gob directly. EncodeLanguageBlob
// clears it on a shallow copy of Language
// before gob-encoding and, only when the map was non-empty, appends the bytes
// from EncodeLargeStateGotosTrailer after the gob payload, inside the same
// decompressed stream, then wrap that gzip payload in the fail-closed envelope
// from language_blob_envelope.go. Blob decoders (gotreesitter.LoadLanguage,
// grammargen's decodeLanguageBlob, and the grammars package loader)
// first unwrap that envelope and then gob-decode the Language exactly as before
// -- which already exactly
// reproduces the pre-trailer behavior for every blob that has no trailer,
// including all 206 blobs checked in as of this change -- and then call
// DecodeLargeStateGotosTrailer on whatever bytes remain, merging the result
// into LargeStateGotos. A slice like []largeStateGotoPair always gob-encodes
// in index order (see encoding/gob's encodeArray, which walks indices
// 0..N-1 with no randomization), so sorting by Key before encoding makes the
// trailer -- and therefore the whole blob -- byte-identical across runs. The
// envelope deliberately makes older gzip-first runtimes reject a new
// trailer-bearing blob instead of accepting it with the map silently missing.
type largeStateGotoPair struct {
	Key    uint64
	Target StateID
}

// EncodeLargeStateGotosTrailer serializes m as a self-contained gob stream of
// (key, target) pairs sorted by Key ascending, suitable for deterministic
// appending after a Language's gob-encoded blob payload (see
// DecodeLargeStateGotosTrailer). It returns (nil, nil) for an empty map so
// callers can skip writing a trailer entirely, leaving the blob byte-for-byte
// identical to one for a Language that never populated LargeStateGotos.
func EncodeLargeStateGotosTrailer(m map[uint64]StateID) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	pairs := make([]largeStateGotoPair, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, largeStateGotoPair{Key: k, Target: v})
	}
	slices.SortFunc(pairs, func(a, b largeStateGotoPair) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		default:
			return 0
		}
	})

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(pairs); err != nil {
		return nil, fmt.Errorf("encode large-state-gotos trailer: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeLargeStateGotosTrailer reads a trailer written by
// EncodeLargeStateGotosTrailer from r, which must be positioned immediately
// after a Language's gob message within the same decompressed blob stream
// (callers must gob-decode from a *bytes.Reader, not directly from a
// gzip.Reader: gob's Decoder can read ahead past its own message boundary on
// a streaming reader, silently discarding trailer bytes it never needed --
// bytes.Reader has no such read-ahead and leaves r positioned exactly at the
// end of the decoded message).
//
// It returns (nil, nil) when r has no remaining bytes -- the common case for
// every blob whose Language never populated LargeStateGotos, including every
// blob encoded before this trailer mechanism existed.
func DecodeLargeStateGotosTrailer(r *bytes.Reader) (map[uint64]StateID, error) {
	if r == nil || r.Len() == 0 {
		return nil, nil
	}
	var pairs []largeStateGotoPair
	if err := gob.NewDecoder(r).Decode(&pairs); err != nil {
		return nil, fmt.Errorf("decode large-state-gotos trailer: %w", err)
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("decode large-state-gotos trailer: %d trailing bytes", r.Len())
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[uint64]StateID, len(pairs))
	for _, p := range pairs {
		m[p.Key] = p.Target
	}
	return m, nil
}
