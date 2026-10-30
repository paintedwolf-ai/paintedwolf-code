package sourcecatalog

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/litprefilter"
)

func assertLiteralBuilder(t *testing.T, input []byte, chunk int) {
	t.Helper()
	stream := newLiteralBloomStream(int64(len(input)))
	for from := 0; from < len(input); from += chunk {
		_, _ = stream.Write(input[from:min(from+chunk, len(input))])
	}
	got, valid := stream.finish()
	wantValid := utf8.Valid(input) && !bytes.ContainsRune(input, 0)
	if valid != wantValid {
		t.Fatalf("validity input=%x chunk=%d: got %v, want %v", input, chunk, valid, wantValid)
	}
	if !valid {
		if got != nil {
			t.Fatal("invalid content retained a pruning filter")
		}
		return
	}
	folded := litprefilter.AppendCanonicalFold(nil, input)
	want := newLiteralBloom(int64(len(input)))
	for i := 0; i+2 < len(folded); i++ {
		want.add([3]byte{folded[i], folded[i+1], folded[i+2]})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("filter differs from complete canonical trigrams: chunk=%d input=%x", chunk, input)
	}
}

func TestLiteralBuilderMatchesEveryBytePair(t *testing.T) {
	for first := 0; first < 256; first++ {
		for second := 0; second < 256; second++ {
			input := []byte{'x', byte(first), byte(second), 'Y', 'z'}
			for _, chunk := range []int{1, 2, len(input)} {
				assertLiteralBuilder(t, input, chunk)
			}
		}
	}
}

func TestLiteralBuilderMatchesCanonicalTrigramsAcrossChunks(t *testing.T) {
	inputs := [][]byte{
		nil, {}, []byte("a"), []byte("ab"),
		[]byte("ASCII Kelvin ſource Σigma İSTANBUL élan \ufffd \U00010000 \U0010ffff\n"),
		{0xc2}, {0xe0, 0xa0}, {0xf0, 0x90, 0x80},
		{0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80},
		{0xef, 0xbf, 0xbd, 0},
	}
	rng := rand.New(rand.NewPCG(921, 1003))
	var text []byte
	for range 1024 {
		r := rune(rng.IntN(utf8.MaxRune + 1))
		if utf8.ValidRune(r) && r != 0 {
			text = utf8.AppendRune(text, r)
		}
	}
	inputs = append(inputs, text, bytes.Repeat([]byte("abcKeΣ\n"), literalReadQuantum/4))
	for _, input := range inputs {
		for _, chunk := range []int{1, 2, 3, 4, 5, 127, literalReadQuantum - 1, literalReadQuantum, len(input) + 1} {
			assertLiteralBuilder(t, input, chunk)
		}
	}
}

func TestLiteralBuilderRetainsOnlySplitRuneAndFilter(t *testing.T) {
	input := bytes.Repeat([]byte("abcKeΣ\n"), literalReadQuantum)
	stream := newLiteralBloomStream(int64(len(input)))
	allocations := testing.AllocsPerRun(5, func() {
		clear(stream.bloom)
		stream.window, stream.filled, stream.pendingN = [3]byte{}, 0, 0
		for from := 0; from < len(input); from += 127 {
			_, _ = stream.Write(input[from:min(from+127, len(input))])
		}
	})
	if allocations != 0 {
		t.Fatalf("stream allocated temporary storage: %g", allocations)
	}
	if _, valid := stream.finish(); !valid {
		t.Fatal("complete valid content was rejected")
	}
	_, _ = stream.Write([]byte{0xf0, 0x90, 0x80})
	if stream.pendingN != 3 {
		t.Fatalf("split rune retained %d bytes, want 3", stream.pendingN)
	}
	if _, valid := stream.finish(); valid {
		t.Fatal("truncated rune supplied a pruning filter")
	}
}
