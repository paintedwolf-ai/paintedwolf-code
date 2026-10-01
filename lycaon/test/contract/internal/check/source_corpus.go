package check

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/testcorpus"
)

// SourceLoader caches source corpora across the contract suites in one test binary.
var SourceLoader testcorpus.Loader

func WalkFiles(root string, exts map[string]struct{}, skipTestGo bool, fn func(path string, data []byte) error) error {
	extensions := make([]string, 0, len(exts))
	for ext := range exts {
		extensions = append(extensions, ext)
	}
	corp, err := SourceLoader.Load(root, testcorpus.Options{Extensions: extensions})
	if err != nil {
		return err
	}
	files := corp.Files()
	if skipTestGo {
		files = corp.Select(func(file testcorpus.File) bool {
			return !strings.HasSuffix(file.Rel, "_test.go")
		})
	}
	if len(files) == 0 {
		return fmt.Errorf("source corpus selection is empty: %s", root)
	}
	for _, file := range files {
		if err := fn(file.Path, file.Bytes()); err != nil {
			return err
		}
	}
	return nil
}
