package check

import (
	"fmt"
	"go/parser"

	"github.com/lycaon/lycaon/pkg/testcorpus"
)

var contractGoLoader testcorpus.GoLoader

func LoadGoASTCorpus(root string) (*testcorpus.GoCorpus, error) {
	return LoadGoASTCorpusMode(root, parser.ParseComments|parser.SkipObjectResolution)
}

func LoadGoASTCorpusMode(root string, mode parser.Mode) (*testcorpus.GoCorpus, error) {
	corpus, err := contractGoLoader.Load(root, testcorpus.Options{
		SkipDirectories: []string{".git", "node_modules", "testdata", "vendor"},
	}, mode)
	if err != nil {
		return nil, err
	}
	if len(corpus.Files()) == 0 {
		return nil, fmt.Errorf("go corpus selection is empty: %s", root)
	}
	return corpus, nil
}
