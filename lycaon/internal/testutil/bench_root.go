package testutil

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/scalefixture"
)

// benchFallbackFiles keeps a benchmark run outside the performance lane short.
const benchFallbackFiles = 2_000

// BenchRoot is the tree a benchmark measures, read-only: PW_BENCH_ROOT, which
// the performance lane sets to the scale fixture's tree, or else a small tree
// of the same shape.
func BenchRoot(b *testing.B) string {
	b.Helper()
	if root := os.Getenv("PW_BENCH_ROOT"); root != "" {
		return root
	}
	root := b.TempDir()
	_, err := scalefixture.WriteTree(root, benchFallbackFiles)
	FailErr(b, "write benchmark tree", err)
	return root
}
