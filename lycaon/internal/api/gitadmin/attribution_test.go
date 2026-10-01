package gitadmin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAttributeChangedFileBoundary(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	housePath := filepath.Join(base, "roothouse")
	testutil.FailErr(t, "mkdir root", os.MkdirAll(rootPath, 0o755))
	testutil.FailErr(t, "mkdir house", os.MkdirAll(housePath, 0o755))
	refs := []projectroot.RootRef{{ID: "r1", Path: rootPath}}
	id, rel := attributeChangedFile(base, "roothouse/x.go", refs)
	if id != "" || rel != "" {
		t.Fatalf("boundary leak: id=%q rel=%q", id, rel)
	}
	id, rel = attributeChangedFile(base, "root/x.go", refs)
	if id != "r1" || rel != "x.go" {
		t.Fatalf("under root: id=%q rel=%q", id, rel)
	}
}
