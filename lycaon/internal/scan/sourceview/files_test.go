package sourceview

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMaterializePreservesOtherFilesAfterUnsupportedScript(t *testing.T) {
	project, output := t.TempDir(), t.TempDir()
	files := map[string]string{
		"valid.html":      "<script>eval(location.hash)</script>",
		"broken.html":     "<script>eval(location.hash)",
		"unsupported.vue": "<script lang=\"coffee\">value = 1</script>",
	}
	for name, source := range files {
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(project, name), []byte(source), 0o600))
	}
	origins, limitations, err := Materialize(t.Context(), project, output, []string{"broken.html", "valid.html", "unsupported.vue"})
	testutil.FailErr(t, "materialize independent files", err)
	if len(origins) != 1 || len(limitations) != 2 || limitations[0].File != "broken.html" || limitations[1].File != "unsupported.vue" {
		t.Fatalf("origins=%v limitations=%v", origins, limitations)
	}
	for path, original := range origins {
		data, err := os.ReadFile(path)
		testutil.FailErr(t, "read projection", err)
		if original.Path != "valid.html" || !bytes.Contains(data, []byte("eval(location.hash)")) {
			t.Fatalf("lost valid source: %s %q", original.Path, data)
		}
	}
}

func TestMaterializeDoesNotDowngradeFilesystemErrors(t *testing.T) {
	if _, _, err := Materialize(t.Context(), t.TempDir(), t.TempDir(), []string{"missing.html"}); err == nil {
		t.Fatal("missing snapshot file became an analysis limitation")
	}
}
