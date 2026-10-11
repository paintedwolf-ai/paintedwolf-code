package sourcecatalog

import (
	"bufio"
	"context"
	"errors"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/textfile"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type literalBloom []uint64

func buildLiteralBloom(
	ctx context.Context,
	entry Entry,
	open func(Entry) (io.ReadCloser, error),
) (literalBloom, bool, error) {
	f, err := open(entry)
	if err != nil {
		return literalBloom{}, false, err
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64*1024)
	prefix, _ := reader.Peek(3)
	var decoded io.Reader = reader
	if len(prefix) >= 2 && prefix[0] == 0xff && prefix[1] == 0xfe {
		decoded = transform.NewReader(reader, unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder())
	} else if len(prefix) >= 2 && prefix[0] == 0xfe && prefix[1] == 0xff {
		decoded = transform.NewReader(reader, unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder())
	} else if len(prefix) >= 3 && prefix[0] == 0xef && prefix[1] == 0xbb && prefix[2] == 0xbf {
		_, _ = reader.Discard(3)
	}
	return buildLiteralBloomReader(ctx, decoded, entry.Size)
}

// buildLiteralBloomReader carries split UTF-8 runes across chunks before folding.
func buildLiteralBloomReader(ctx context.Context, reader io.Reader, contentBytes int64) (literalBloom, bool, error) {
	bloom := newLiteralBloom(contentBytes)
	validator := textfile.UTF8Validator{}
	window := [3]byte{}
	filled := 0
	buf := make([]byte, 64*1024)
	folded := make([]byte, 0, 64*1024+8)
	var carry []byte
	for {
		if err := ctx.Err(); err != nil {
			return literalBloom{}, false, err
		}
		n, readErr := reader.Read(buf)
		atEOF := errors.Is(readErr, io.EOF)
		if !validator.Add(buf[:n], atEOF) {
			return literalBloom{}, false, nil
		}
		chunk := buf[:n]
		if len(carry) > 0 {
			chunk = append(carry, chunk...)
		}
		complete := chunk
		if !atEOF {
			complete = trimIncompleteUTF8Tail(chunk)
		}
		folded = litprefilter.AppendCanonicalFold(folded[:0], complete)
		carry = append(carry[:0], chunk[len(complete):]...)
		for _, b := range folded {
			if filled < len(window) {
				window[filled] = b
				filled++
				if filled < len(window) {
					continue
				}
			} else {
				window[0], window[1], window[2] = window[1], window[2], b
			}
			bloom.add(window)
		}
		if atEOF {
			return bloom, true, nil
		}
		if readErr != nil {
			return literalBloom{}, false, readErr
		}
	}
}

// trimIncompleteUTF8Tail drops up to three trailing bytes of a codepoint cut
// by a read boundary.
func trimIncompleteUTF8Tail(raw []byte) []byte {
	for removed := 0; removed < utf8.UTFMax-1 && len(raw) > 0; removed++ {
		if r, size := utf8.DecodeLastRune(raw); r != utf8.RuneError || size > 1 {
			break
		}
		raw = raw[:len(raw)-1]
	}
	return raw
}

func literalBloomWords(contentBytes int64) int {
	words := literalBloomMinWords
	target := int(contentBytes / 64) // one Bloom bit per content byte
	for words < target && words < literalBloomMaxWords {
		words *= 2
	}
	if words > literalBloomMaxWords {
		words = literalBloomMaxWords
	}
	return words
}

func (b *literalBloom) add(gram [3]byte) {
	if len(*b) == 0 {
		return
	}
	h := literalGramHash(gram)
	for _, shift := range [...]uint{0, 21, 42} {
		bit := (h >> shift) & literalBloomMask(len(*b))
		(*b)[bit/64] |= uint64(1) << (bit % 64)
	}
}

// foldedRequirement is a requirement in the bloom's canonical fold.
type foldedRequirement [][][]byte

func foldRequirement(req litprefilter.Requirement) foldedRequirement {
	out := make(foldedRequirement, 0, len(req.Clauses))
	for _, clause := range req.Clauses {
		folded := make([][]byte, 0, len(clause))
		for _, lit := range clause {
			folded = append(folded, litprefilter.AppendCanonicalFold(nil, lit.Bytes))
		}
		out = append(out, folded)
	}
	return out
}

// admits reports whether the file may satisfy every clause: a clause rules the
// file out only when the bloom excludes each of its literals.
func (b literalBloom) admits(req foldedRequirement) bool {
	for _, clause := range req {
		if !slices.ContainsFunc(clause, b.mayContain) {
			return false
		}
	}
	return true
}

func (b literalBloom) mayContain(folded []byte) bool {
	if len(b) == 0 {
		return false
	}
	if len(folded) < 3 {
		return true
	}
	for i := 0; i+2 < len(folded); i++ {
		gram := [3]byte{folded[i], folded[i+1], folded[i+2]}
		h := literalGramHash(gram)
		for _, shift := range [...]uint{0, 21, 42} {
			bit := (h >> shift) & literalBloomMask(len(b))
			if b[bit/64]&(uint64(1)<<(bit%64)) == 0 {
				return false
			}
		}
	}
	return true
}

func literalBloomMask(words int) uint64 {
	return uint64(words*64 - 1) //nolint:gosec // Bloom length is capped.
}

func literalGramHash(gram [3]byte) uint64 {
	x := uint64(gram[0])<<16 | uint64(gram[1])<<8 | uint64(gram[2])
	x ^= x >> 13
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	return x ^ (x >> 33)
}

func newLiteralBloom(contentBytes int64) literalBloom {
	return make(literalBloom, literalBloomWords(contentBytes))
}
