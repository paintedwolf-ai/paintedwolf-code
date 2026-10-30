package sourcecatalog

import (
	"context"
	"errors"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/litprefilter"
)

var errLiteralReadIncomplete = errors.New("content observation was incomplete")

type literalBloom []uint64

func buildLiteralBloom(
	ctx context.Context,
	entry Entry,
	open func(Entry) (io.ReadCloser, error),
) (literalFileBlooms, error) {
	if err := ctx.Err(); err != nil {
		return literalFileBlooms{}, err
	}
	f, err := open(entry)
	if err != nil {
		return literalFileBlooms{}, err
	}
	defer func() { _ = f.Close() }()
	builder := newLiteralContent(entry.Size)
	var total int64
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return literalFileBlooms{}, err
		}
		n, err := f.Read(buf)
		total += int64(n)
		if total > entry.Size {
			return literalFileBlooms{}, errLiteralReadIncomplete
		}
		if _, buildErr := builder.Write(buf[:n]); buildErr != nil {
			return literalFileBlooms{}, buildErr
		}
		if errors.Is(err, io.EOF) {
			if total != entry.Size {
				return literalFileBlooms{}, errLiteralReadIncomplete
			}
			return builder.finish()
		}
		if err != nil {
			return literalFileBlooms{}, err
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
			if !utf8.Valid(lit.Bytes) {
				folded = append(folded, nil)
			} else {
				folded = append(folded, litprefilter.AppendCanonicalFold(nil, lit.Bytes))
			}
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

func (b literalBloom) admitsRequirement(req litprefilter.Requirement) bool {
	var buf [128]byte
	for _, clause := range req.Clauses {
		matched := false
		for _, lit := range clause {
			if !utf8.Valid(lit.Bytes) || b.mayContain(litprefilter.AppendCanonicalFold(buf[:0], lit.Bytes)) {
				matched = true
				break
			}
		}
		if !matched {
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
