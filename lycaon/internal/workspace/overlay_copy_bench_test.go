package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildBenchTree writes dirs×filesPerDir files of fileBytes each under a temp dir.
func buildBenchTree(b *testing.B, dirs, filesPerDir, fileBytes int) string {
	b.Helper()
	root := b.TempDir()
	blob := make([]byte, fileBytes)
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		for f := 0; f < filesPerDir; f++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", f)), blob, 0o644); err != nil {
				b.Fatalf("write: %v", err)
			}
		}
	}
	return root
}

// BenchmarkMirrorTree compares workspace provisioning across copy-pool sizes.
func BenchmarkMirrorTree(b *testing.B) {
	src := buildBenchTree(b, 40, 50, 16<<10) // 2000 files, 16 KiB each (~31 MiB)
	saved := workspaceMirrorConcurrency
	b.Cleanup(func() { workspaceMirrorConcurrency = saved })

	for _, workers := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			workspaceMirrorConcurrency = workers
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst := filepath.Join(b.TempDir(), "workspace")
				if _, err := mirrorTree(context.Background(), src, dst, mirrorOptions{
					phase: "benchmark", tolerateChange: true,
				}); err != nil {
					b.Fatalf("mirrorTree: %v", err)
				}
			}
		})
	}
}
