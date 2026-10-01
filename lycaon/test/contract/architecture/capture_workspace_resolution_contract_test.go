package contract

import (
	"path/filepath"
	"strconv"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPageToolsDoNotBypassWorkspaceAwarePathResolution(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "tools", "native", "page")
	corpus, err := contractcheck.LoadGoASTCorpus(root)
	contractcheck.FailErr(t, "load native page Go corpus", err)

	const bypass = "github.com/lycaon/lycaon/internal/projectroot"
	for _, file := range corpus.Files() {
		if file.IsTest {
			continue
		}
		for _, spec := range file.AST.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			contractcheck.FailErr(t, "decode native page import", err)
			if path == bypass {
				t.Errorf("%s imports projectroot directly; page targets must resolve through projectpaths so worker captures read their isolated workspace", file.Rel)
			}
		}
	}
}
