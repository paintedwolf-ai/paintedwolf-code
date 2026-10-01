package gotreesitter

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
)

// LoadLanguage deserializes a compressed grammar blob into a Language.
// Blobs are produced by EncodeLanguageBlob, grammargen.Generate, or the
// grammar build toolchain. This is the only function needed at runtime to load
// pre-compiled grammars — no grammargen import required. It accepts legacy
// gzip blobs and version-enveloped trailer-bearing blobs.
func LoadLanguage(data []byte) (*Language, error) {
	compressed, expectsTrailer, err := UnwrapLanguageBlobEnvelope(data)
	if err != nil {
		return nil, fmt.Errorf("decode language: %w", err)
	}
	gzr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gzr.Close()

	raw, err := ReadAllGzipWithSizeHint(gzr, compressed)
	if err != nil {
		return nil, fmt.Errorf("read gzip: %w", err)
	}

	var lang Language
	br := bytes.NewReader(raw)
	if err := gob.NewDecoder(br).Decode(&lang); err != nil {
		return nil, fmt.Errorf("decode language: %w", err)
	}
	// LargeStateGotos (when non-empty) is never gob-encoded directly -- see
	// large_state_gotos_trailer.go for why -- so restore it from the trailer
	// appended after the gob message, if the encoder wrote one. This is a
	// cheap no-op (returns immediately) for every blob without a trailer,
	// including all blobs encoded before this trailer mechanism existed.
	trailer, err := DecodeLargeStateGotosTrailer(br)
	if err != nil {
		return nil, fmt.Errorf("decode language: %w", err)
	}
	if expectsTrailer && len(trailer) == 0 {
		return nil, fmt.Errorf("decode language: envelope requires a non-empty large-state-gotos trailer")
	}
	if !expectsTrailer && len(trailer) != 0 {
		return nil, fmt.Errorf("decode language: large-state-gotos trailer requires a versioned envelope")
	}
	if trailer != nil {
		lang.LargeStateGotos = trailer
	}
	lang.grammarBlobSHA256 = sha256.Sum256(data)
	lang.grammarBlobSHA256Valid = true
	InferGeneratedRepeatAuxMetadata(&lang)

	return &lang, nil
}

// gzipSizeHintCap bounds the ISIZE-derived pre-allocation in
// ReadAllGzipWithSizeHint. It guards against a corrupted or adversarial
// trailer requesting an unbounded allocation. The bound is set comfortably
// above the largest known compiled grammar table (Swift's ~308 MB
// decompressed gob stream, the largest of the 206 shipped grammars as of
// v0.43.1 — its lexer DFA is compiled per lex-mode with no cross-mode
// automaton sharing, and Swift's grammar has ~331 distinct lex modes versus
// single digits to low tens for most languages, so its LexStates table is
// disproportionately large; see grammars/language_memory_ceiling_test.go)
// with headroom for grammar growth, while still catching a bogus ISIZE field
// long before it could request a multi-gigabyte buffer.
const gzipSizeHintCap = 512 * 1024 * 1024 // 512 MB

// ReadAllGzipWithSizeHint reads all of r — an open gzip.Reader positioned at
// the start of the member whose raw (still-compressed) bytes are compressed
// — into memory, pre-sizing the destination buffer from the gzip ISIZE
// trailer (the last 4 bytes of compressed) instead of letting io.ReadAll grow
// the buffer by repeated doubling. ISIZE is the uncompressed size mod 2^32,
// which is exact for every blob under 4 GB; grammar blobs are always far
// smaller. Falls back to io.ReadAll when the hint is missing, zero, or
// exceeds gzipSizeHintCap.
//
// This matters at grammar-load time: io.ReadAll's doubling growth roughly
// doubles peak transient allocation versus the final size, and for the
// largest shipped grammar blobs that transient churn is measured in hundreds
// of MB to low GB (observed via alloc-space pprof on Language() calls),
// which is what actually trips container memory limits even though the
// final retained Language is smaller.
func ReadAllGzipWithSizeHint(r io.Reader, compressed []byte) ([]byte, error) {
	if len(compressed) >= 4 {
		isize := binary.LittleEndian.Uint32(compressed[len(compressed)-4:])
		if isize > 0 && isize < gzipSizeHintCap {
			raw := make([]byte, 0, isize)
			var buf [32 * 1024]byte
			for {
				n, readErr := r.Read(buf[:])
				if n > 0 {
					raw = append(raw, buf[:n]...)
				}
				if readErr == io.EOF {
					return raw, nil
				}
				if readErr != nil {
					return nil, readErr
				}
			}
		}
	}
	return io.ReadAll(r)
}
