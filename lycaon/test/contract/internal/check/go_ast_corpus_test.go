package check_test

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestLoadGoASTCorpusSubtreeIgnoresMalformedSibling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modDir := filepath.Join(root, "lycaon")
	goodDir := filepath.Join(modDir, "internal", "good")
	badDir := filepath.Join(modDir, "internal", "broken")
	for _, dir := range []string{goodDir, badDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte("module example.com/lycaon\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "ok.go"), []byte("package good\n\nfunc OK() {}\n"), 0o600); err != nil {
		t.Fatalf("write ok.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "bad.go"), []byte("package\n"), 0o600); err != nil {
		t.Fatalf("write bad.go: %v", err)
	}

	if _, err := contractcheck.LoadGoASTCorpus(modDir); err == nil {
		t.Fatal("LoadGoASTCorpus(modDir) succeeded despite malformed file")
	}
	corpus, err := contractcheck.LoadGoASTCorpus(goodDir)
	if err != nil {
		t.Fatalf("LoadGoASTCorpus(goodDir) failed due to malformed sibling: %v", err)
	}
	files := corpus.Files()
	if len(files) != 1 || files[0].Rel != "ok.go" {
		t.Fatalf("goodDir files = %+v, want [ok.go]", files)
	}
}
