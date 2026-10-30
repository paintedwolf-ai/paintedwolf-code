package sourcecatalog

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
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
