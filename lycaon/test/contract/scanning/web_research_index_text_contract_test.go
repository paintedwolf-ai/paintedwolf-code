package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/webindex"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWebIndexQueuePathsNormalizeText locks the index plain-text write boundary.
func TestWebIndexQueuePathsNormalizeText(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	storePath := filepath.Join(root, "lycaon", "internal", "webindex", "store.go")
	body, err := os.ReadFile(storePath)
	contractcheck.FailErr(t, "read file", err)
	src := string(body)
	if !strings.Contains(src, "NormalizeWebTextForStorage(p.Title") {
		t.Fatalf("%s QueuePage must normalize Title", storePath)
	}
	if !strings.Contains(src, "NormalizeWebTextForStorage(p.Description") {
		t.Fatalf("%s QueuePage must normalize Description", storePath)
	}
	if !strings.Contains(src, "NormalizeWebTextForStorage(text") {
		t.Fatalf("%s QueueAnchors must normalize anchor texts", storePath)
	}

	got := webindex.NormalizeWebTextForStorage(`<b>Hi&nbsp;there</b>`, 0)
	if got != "Hi there" {
		t.Fatalf("NormalizeWebTextForStorage = %q", got)
	}
	if strings.Contains(got, "<") {
		t.Fatalf("normalized text still has markup: %q", got)
	}

	providers := []string{
		"brave.go", "tavily.go", "searxng.go", "kagi.go",
		"serper.go", "google_cse.go", "github.go", "gitlab.go", "marginalia.go", "mwmbl.go",
	}
	for _, name := range providers {
		p := filepath.Join(root, "lycaon", "internal", "webresearch", name)
		b, err := os.ReadFile(p)
		contractcheck.FailErr(t, "read file", err)
		if strings.Contains(string(b), "StripHTML(") {
			t.Fatalf("%s must not call StripHTML", p)
		}
		if !strings.Contains(string(b), "webindex.NormalizeWebTextForStorage(") {
			t.Fatalf("%s must route titles/snippets through webindex.NormalizeWebTextForStorage", p)
		}
	}
}
