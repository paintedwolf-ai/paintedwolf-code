package check

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RustModuleSource includes an entry module and its production submodules.
func RustModuleSource(t *testing.T, root, relative string) string {
	t.Helper()
	entry := filepath.Join(root, relative)
	var source strings.Builder
	raw, err := os.ReadFile(entry)
	FailErr(t, "read Rust entry module", err)
	source.Write(raw)
	directory := strings.TrimSuffix(entry, ".rs")
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		return source.String()
	} else {
		FailErr(t, "inspect Rust module directory", err)
	}
	err = filepath.WalkDir(directory, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() || !strings.HasSuffix(path, ".rs") || item.Name() == "tests.rs" || strings.HasSuffix(path, "_test.rs") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source.WriteByte('\n')
		source.Write(body)
		return nil
	})
	FailErr(t, "read Rust submodules", err)
	return source.String()
}
