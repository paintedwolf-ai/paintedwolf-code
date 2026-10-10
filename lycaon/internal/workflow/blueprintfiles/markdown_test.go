package blueprintfiles

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBlueprintViewAndContentUsePersistedMarkdown(t *testing.T) {
	root := t.TempDir()
	content := "---\ntitle: Release plan\ncount: 2\nprivate: secret\n---\nBody\n"
	if err := WriteBlueprintFile(root, "plans/release.md", content); err != nil {
		t.Fatal(err)
	}
	view, err := LoadBlueprintView(root, &workflowdef.BlueprintDef{Path: "plans/release.md", Frontmatter: []string{" title ", "count", "", "missing"}})
	if err != nil || !reflect.DeepEqual(view, map[string]string{"title": "Release plan", "count": "2"}) {
		t.Fatalf("view=%v err=%v", view, err)
	}
	if got := ParsePlanBlueprintFrontmatter(content, []string{"title", "count"}); !reflect.DeepEqual(got, view) {
		t.Fatalf("inline and persisted views disagree: %v", got)
	}
	got, err := ResolveContent(t.Context(), &api.WorkflowRun{BlueprintPath: "plans/release.md"}, root, "")
	if err != nil || got != content {
		t.Fatalf("resolved content=%q err=%v", got, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := WriteBlueprintFile(root, "linked/escape.md", content); err == nil {
		t.Fatal("write followed symlink outside project")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.md")); !os.IsNotExist(err) {
		t.Fatalf("outside changed: %v", err)
	}
	if got := ParsePlanBlueprintFrontmatter("No frontmatter", []string{"title"}); len(got) != 0 {
		t.Fatalf("unstructured content inferred metadata: %v", got)
	}
}
