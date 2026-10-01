package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeResourceFiles(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	for i := 0; i < n; i++ {
		name := "f" + pad3(i) + ".md"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write resource", err)
		}
	}
}

func TestDiscoverBundledResourcesListingBound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("s"), 0o644); err != nil {
		testutil.FailErr(t, "write SKILL.md", err)
	}
	writeResourceFiles(t, filepath.Join(dir, "references"), ResourceListMax+5)

	catalog := DiscoverBundledResources(os.DirFS(dir), ".")
	if len(catalog.Visible) != ResourceListMax {
		t.Fatalf("listed=%d want %d", len(catalog.Visible), ResourceListMax)
	}
	if catalog.Omitted != 5 {
		t.Fatalf("omitted=%d want 5", catalog.Omitted)
	}
	for _, rel := range catalog.Visible {
		if rel == "SKILL.md" {
			t.Fatal("SKILL.md must not be listed as a resource")
		}
	}
}

func TestDiscoverBundledResourcesWalkBound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("s"), 0o644); err != nil {
		testutil.FailErr(t, "write SKILL.md", err)
	}
	total := ResourceWalkMax + 100
	writeResourceFiles(t, filepath.Join(dir, "assets"), total)

	catalog := DiscoverBundledResources(os.DirFS(dir), ".")
	if len(catalog.Visible) != ResourceListMax {
		t.Fatalf("listed=%d want %d", len(catalog.Visible), ResourceListMax)
	}
	// Omitted counts only visited entries.
	if catalog.Omitted > ResourceWalkMax-ResourceListMax {
		t.Fatalf("omitted=%d exceeds walk bound %d", catalog.Omitted, ResourceWalkMax-ResourceListMax)
	}
	if catalog.Omitted <= 0 {
		t.Fatalf("omitted=%d want > 0", catalog.Omitted)
	}
}

func TestSkillReadResourceRejectsSymlinkSwapOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	resource := filepath.Join(dir, "reference.md")
	if err := os.WriteFile(resource, []byte("inside"), 0o644); err != nil {
		testutil.FailErr(t, "write resource", err)
	}
	resourceFS := os.DirFS(dir)
	sk := Skill{Name: "safe"}
	sk.BindResources(resourceFS, ".", DiscoverBundledResources(resourceFS, "."), dir)
	body, err := sk.ReadResource("reference.md")
	if err != nil || string(body) != "inside" {
		t.Fatalf("initial read = %q, %v", body, err)
	}
	if err := os.Remove(resource); err != nil {
		testutil.FailErr(t, "remove resource", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		testutil.FailErr(t, "write external resource", err)
	}
	if err := os.Symlink(outside, resource); err != nil {
		testutil.FailErr(t, "replace resource with symlink", err)
	}
	if _, err := sk.ReadResource("reference.md"); err == nil {
		t.Fatal("root-confined resource read followed a swapped external symlink")
	}
}

func TestSkillSourcePathUsesBoundResourceCatalog(t *testing.T) {
	root := t.TempDir()
	sk := Skill{Dir: "/display-only"}
	if sk.SourcePath("") != "" {
		t.Fatal("display-only directory supplied a physical location")
	}
	sk.BindResources(os.DirFS(root), ".", ResourceCatalog{Readable: []string{"refs/guide.md"}}, root)
	if sk.SourcePath("") != filepath.Join(root, "SKILL.md") || sk.SourcePath("refs/guide.md") != filepath.Join(root, "refs/guide.md") {
		t.Fatal("bound resource identity was lost")
	}
	for _, resource := range []string{"../outside", "unknown.md", "."} {
		if sk.SourcePath(resource) != "" {
			t.Fatalf("unadmitted resource %q supplied a location", resource)
		}
	}
}
