package sourceview

import (
	"path/filepath"
	"testing"
)

func TestResolveSymlinkTargetRelAllowsDotsInsideAComponent(t *testing.T) {
	root := t.TempDir()
	got := SymlinkTarget(root, "docs/link", "notes..draft.md")
	if got != "docs/notes..draft.md" {
		t.Fatalf("resolved dotted filename = %q", got)
	}
	if got := SymlinkTarget(root, "docs/link", "../../outside"); got != "" {
		t.Fatalf("resolved escaping target = %q", got)
	}
	outside := filepath.Join(root, "..", "outside")
	if got := SymlinkTarget(root, "docs/link", outside); got != "" {
		t.Fatalf("resolved absolute escaping target = %q", got)
	}
}
