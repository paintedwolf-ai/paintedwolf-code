package checkpoint

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestJournalLocationRequiresExactLocalIdentity(t *testing.T) {
	for _, id := range []string{"", "..", "../journal", "/journal", "a/b", `a\b`, ".hidden", " operation "} {
		if _, err := JournalRelPath("/project", id); err == nil {
			t.Errorf("accepted operation identity %q", id)
		}
		if _, err := LoadOperationJournal(t.TempDir(), store.RewindOperation{ID: id, ProjectDir: "/project"}); err == nil {
			t.Errorf("loaded invalid identity %q", id)
		}
	}
	if _, err := JournalRelPath("", "operation"); err == nil {
		t.Fatal("accepted missing project")
	}
	rel, err := JournalRelPath("/project", "operation")
	testutil.FailErr(t, "resolve journal", err)
	if !filepath.IsLocal(rel) {
		t.Fatalf("journal is not installation-relative: %q", rel)
	}
}
