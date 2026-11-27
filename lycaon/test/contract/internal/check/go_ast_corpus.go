package check

import (
	"fmt"
	"go/parser"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/pkg/testcorpus"
)

const standardGoCorpusMode = parser.ParseComments | parser.SkipObjectResolution

var contractGoLoader testcorpus.GoLoader

func LoadGoASTCorpus(root string) (*testcorpus.GoCorpus, error) {
	return LoadGoASTCorpusMode(root, standardGoCorpusMode)
}

func LoadGoASTCorpusMode(root string, mode parser.Mode) (*testcorpus.GoCorpus, error) {
	opts := testcorpus.Options{
		SkipDirectories: []string{".git", "node_modules", "testdata", "vendor"},
	}
	var (
		corpus *testcorpus.GoCorpus
		err    error
	)
	if mode == standardGoCorpusMode {
		if abs, absErr := filepath.Abs(root); absErr == nil {
			if modRoot := enclosingLycaonModuleRoot(abs); modRoot != "" {
				base, loadErr := contractGoLoader.Load(modRoot, opts, mode)
				if loadErr != nil {
					return nil, loadErr
				}
				corpus, err = base.Sub(abs)
			}
		}
	}
	if corpus == nil && err == nil {
		corpus, err = contractGoLoader.Load(root, opts, mode)
	}
	if err != nil {
		return nil, err
	}
	if len(corpus.Files()) == 0 {
		return nil, fmt.Errorf("go corpus selection is empty: %s", root)
	}
	return corpus, nil
}

// enclosingLycaonModuleRoot returns the enclosing lycaon/ module directory when
// abs lies at or below it, so overlapping subtree requests share one parsed AST
// corpus instead of retaining duplicate full-tree copies.
func enclosingLycaonModuleRoot(abs string) string {
	dir := abs
	for {
		if filepath.Base(dir) == "lycaon" {
			if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
