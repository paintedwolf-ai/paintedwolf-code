package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// CheckoutRoot returns the repository root containing the Go module.
func CheckoutRoot(t testing.TB) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find repository checkout: caller location unavailable")
	}
	for dir := filepath.Dir(filename); ; dir = filepath.Dir(dir) {
		if checkoutMarkersExist(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("find repository checkout: Taskfile.yml and lycaon/go.mod not found")
		}
	}
}

func checkoutMarkersExist(dir string) bool {
	for _, path := range []string{
		filepath.Join(dir, "Taskfile.yml"),
		filepath.Join(dir, "lycaon", "go.mod"),
	} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}
