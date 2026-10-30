package sourcecatalog

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLiteralStreamPreservesEveryChunkBoundary(t *testing.T) {
	content := []byte("CompleteNeedle Kelvin ſource Σigma İSTANBUL élan\n")
	for chunk := 1; chunk <= len(content); chunk++ {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			stream := newLiteralBloomStream(int64(len(content)))
			for from := 0; from < len(content); from += chunk {
				_, _ = stream.Write(content[from:min(from+chunk, len(content))])
			}
			bloom, searchable := stream.finish()
			if !searchable {
				t.Fatal("valid text was rejected")
			}
			for _, literal := range []string{"completeneedle", "kelvin", "source", "σigma", "istanbul", "ÉLAN"} {
				if !bloom.admits(foldRequirement(litprefilter.AnyOf(literal))) {
					t.Fatalf("lost %q at chunk %d", literal, chunk)
				}
			}
		})
	}
	for _, content := range [][]byte{{0}, {0xff}, {0xc3}, {0xe2, 0x82}} {
		stream := newLiteralBloomStream(int64(len(content)))
		for _, b := range content {
			_, _ = stream.Write([]byte{b})
		}
		if _, searchable := stream.finish(); searchable {
			t.Fatalf("accepted invalid text %v", content)
		}
	}
}

func TestLiteralFileProjectionRetentionSharesOneBudget(t *testing.T) {
	var cache literalFileCache
	decoded, raw := newLiteralBloom(1<<30), newLiteralBloom(1<<30)
	for i := range 800 {
		cache.store("/root", fmt.Sprintf("src/%04d.txt", i), literalFileCacheEntry{bloom: decoded, rawBloom: raw})
		if cache.bytes > literalFileByteCap {
			t.Fatalf("byte cap exceeded: %d", cache.bytes)
		}
	}
	if cache.count >= 800 || cache.count == 0 {
		t.Fatalf("retained %d observations", cache.count)
	}
	cache.drop("/root", ".")
	if cache.count != 0 || cache.bytes != 0 {
		t.Fatalf("released cache retained count=%d bytes=%d", cache.count, cache.bytes)
	}
}

func TestContentProjectionsSurviveEncodingAndChunkBoundaries(t *testing.T) {
	for name, content := range map[string]string{"utf8": "Kelvin CompleteNeedle", "bom": "\ufeffKelvin CompleteNeedle", "utf16le": "\xff\xfeC\x00o\x00m\x00p\x00l\x00e\x00t\x00e\x00N\x00e\x00e\x00d\x00l\x00e\x00", "utf16be": "\xfe\xff\x00C\x00o\x00m\x00p\x00l\x00e\x00t\x00e\x00N\x00e\x00e\x00d\x00l\x00e"} {
		for chunk := 1; chunk <= len(content); chunk++ {
			t.Run(fmt.Sprintf("%s/%d", name, chunk), func(t *testing.T) {
				builder := newLiteralContent(int64(len(content)))
				for from := 0; from < len(content); from += chunk {
					_, err := builder.Write([]byte(content[from:min(from+chunk, len(content))]))
					testutil.FailErr(t, "write encoded chunk", err)
				}
				facts, err := builder.finish()
				testutil.FailErr(t, "finish encoded content", err)
				if !facts.decoded.searchable || !facts.decoded.bloom.admits(foldRequirement(litprefilter.AnyOf("completeneedle"))) {
					t.Fatal("decoded projection lost a match")
				}
				if name == "utf8" || name == "bom" {
					if !facts.raw.searchable || !facts.raw.bloom.admits(foldRequirement(litprefilter.AnyOf(content))) {
						t.Fatal("byte projection lost encoded content")
					}
				} else if facts.raw.searchable {
					t.Fatal("binary byte projection supplied a pruning proof")
				}
			})
		}
	}
}

func TestLiteralStreamBoundsCallerWriteQuanta(t *testing.T) {
	stream := newLiteralBloomStream(4 << 20)
	_, err := stream.Write(bytes.Repeat([]byte("é"), 2<<20))
	testutil.FailErr(t, "write large chunk", err)
	if cap(stream.folded) > 2*literalReadQuantum || cap(stream.carry) > 2*literalReadQuantum {
		t.Fatalf("unbounded buffers folded=%d carry=%d", cap(stream.folded), cap(stream.carry))
	}
	if _, searchable := stream.finish(); !searchable {
		t.Fatal("valid large chunk was rejected")
	}
}
