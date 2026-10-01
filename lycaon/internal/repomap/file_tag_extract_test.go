package repomap

import (
	"testing"

	_ "github.com/lycaon/lycaon/internal/repomap/gtsqueries"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestTaggerCacheGoSource(t *testing.T) {
	entry := grammars.DetectLanguage("sample.go")
	if entry == nil {
		t.Fatal("expected go grammar")
	}
	parsed := (&taggerCache{}).tag(t.Context(), *entry, []byte("package sample\nfunc OK() {}\n"))
	if parsed.noTagger || parsed.failure != nil {
		t.Fatalf("parse = %+v", parsed)
	}
	if len(parsed.tags) == 0 {
		t.Fatal("expected definition tags")
	}
}
