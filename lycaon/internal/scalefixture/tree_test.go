package scalefixture_test

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scalefixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteTreeIsDeterministic(t *testing.T) {
	const files = 2*scalefixture.FilesPerDirectory + 7
	var shapes [2]scalefixture.Shape
	var listings [2][]string
	for i := range shapes {
		root := t.TempDir()
		shape, err := scalefixture.WriteTree(root, files)
		testutil.FailErr(t, "write tree", err)
		shapes[i] = shape
		testutil.FailErr(t, "list tree", filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				rel, relErr := filepath.Rel(root, path)
				listings[i] = append(listings[i], rel)
				return relErr
			}
			return err
		}))
	}
	if shapes[0] != shapes[1] || shapes[0].Files != files || shapes[0].Directories != 3 {
		t.Fatalf("shapes = %+v", shapes)
	}
	if len(listings[0]) != files || len(listings[1]) != files {
		t.Fatalf("listed %d and %d files, want %d", len(listings[0]), len(listings[1]), files)
	}
	for i := range listings[0] {
		if listings[0][i] != listings[1][i] {
			t.Fatalf("file %d differs: %s and %s", i, listings[0][i], listings[1][i])
		}
	}
}
