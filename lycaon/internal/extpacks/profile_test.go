package extpacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadProfileRejectsUnknownField(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profiles", "strict.yaml")
	testutil.FailErr(t, "create profiles", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write profile", os.WriteFile(path, []byte("name: strict\ndisable: []\ndisabel: []\n"), 0o600))

	_, err := LoadProfile(root, "strict")
	if err == nil || !strings.Contains(err.Error(), "field disabel not found") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadProfileRejectsMultipleDocuments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profiles", "strict.yaml")
	testutil.FailErr(t, "create profiles", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write profile", os.WriteFile(path, []byte("name: strict\n---\nname: strict\n"), 0o600))

	_, err := LoadProfile(root, "strict")
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadProfileRequiresFilenameName(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profiles", "strict.yaml")
	testutil.FailErr(t, "create profiles", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write profile", os.WriteFile(path, []byte("name: different\n"), 0o600))

	_, err := LoadProfile(root, "strict")
	if err == nil || !strings.Contains(err.Error(), "match the filename") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadProfileRejectsPathSeparators(t *testing.T) {
	for _, name := range []string{"nested/profile", `nested\profile`} {
		if _, err := LoadProfile(t.TempDir(), name); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
}
