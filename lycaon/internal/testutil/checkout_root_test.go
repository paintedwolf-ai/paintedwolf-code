package testutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCheckoutRootContainsRepositoryMarkers(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	for _, path := range []string{
		filepath.Join(root, "Taskfile.yml"),
		filepath.Join(root, "lycaon", "go.mod"),
	} {
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("stat checkout marker %q: %v", path, err)
		} else if !info.Mode().IsRegular() {
			t.Fatalf("checkout marker %q is not a regular file", path)
		}
	}
}
