package native

import (
	"strings"
	"testing"
)

func TestCapOpaqueTailWithinCap(t *testing.T) {
	tail := strings.Repeat("a", 100)
	capped, truncated, orig := CapOpaqueTail(tail, 1024)
	if truncated || orig != 100 || capped != tail {
		t.Fatalf("truncated=%v orig=%d len=%d", truncated, orig, len(capped))
	}
}

func TestCapOpaqueTailOverCap(t *testing.T) {
	tail := strings.Repeat("b", 2048)
	capped, truncated, orig := CapOpaqueTail(tail, 1024)
	if !truncated || orig != 2048 || len(capped) != 1024 {
		t.Fatalf("truncated=%v orig=%d len=%d", truncated, orig, len(capped))
	}
}
