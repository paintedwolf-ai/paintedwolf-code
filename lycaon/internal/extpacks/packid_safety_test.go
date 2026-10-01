package extpacks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, enginepaths.ExtensionsCacheDirName), 0o700); err != nil {
		testutil.FailErr(t, "mkdir cache", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("secret"), 0o600); err != nil {
		testutil.FailErr(t, "seed credentials", err)
	}
	return dir
}

func TestValidatePackIDRejectsEscapes(t *testing.T) {
	bad := []string{"", "  ", ".", "..", "../..", `..\..`, "/..", "../evil", "acme/..", ".hidden", "acme/.git"}
	for _, id := range bad {
		if err := ValidatePackID(id); err == nil {
			t.Errorf("ValidatePackID(%q) = nil, want error", id)
		}
	}
	good := []string{"acme/basic", "painted-wolf/platform", "acme/sub/leaf", "acme-io/plan_v2"}
	for _, id := range good {
		if err := ValidatePackID(id); err != nil {
			t.Errorf("ValidatePackID(%q) = %v, want nil", id, err)
		}
	}
}

func TestRemoveRejectsTraversalPackID(t *testing.T) {
	dir := seedConfigDir(t)
	for _, id := range []string{"..", ".", `..\`} {
		if _, err := PrepareRemoval([]string{id}); err == nil {
			t.Errorf("PrepareRemoval(%q) = nil, want error", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil {
		testutil.FailErr(t, "config dir survived remove", err)
	}
	if _, err := os.Stat(filepath.Join(dir, enginepaths.ExtensionsCacheDirName)); err != nil {
		testutil.FailErr(t, "cache survived remove", err)
	}
}

func TestInstallRejectsTraversalManifestID(t *testing.T) {
	dir := seedConfigDir(t)
	src := t.TempDir()
	manifest := "manifest_version: 1\nid: ..\nname: Evil\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	if err := os.WriteFile(filepath.Join(src, "extension.yaml"), []byte(manifest), 0o600); err != nil {
		testutil.FailErr(t, "write manifest", err)
	}
	if plan, err := PrepareInstall(t.Context(), InstallOptions{Source: "path:" + src}); err == nil {
		plan.Close()
		t.Fatal("install with manifest id \"..\" = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil {
		testutil.FailErr(t, "config dir survived install", err)
	}
}

func TestCachedPackRevisionDirRejectsTraversal(t *testing.T) {
	seedConfigDir(t)
	if _, err := CachedPackRevisionDir("..", "abc"); err == nil {
		t.Fatal("CachedPackRevisionDir accepted traversal")
	}
	got, err := CachedPackRevisionDir("acme/basic", "abc")
	if err != nil {
		testutil.FailErr(t, "CachedPackRevisionDir", err)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(got))) != PackIDSafe("acme/basic") || filepath.Base(got) != PackIDSafe("abc") {
		t.Fatalf("CachedPackRevisionDir = %q, want hashed package and revision", got)
	}
	if PackIDSafe("acme/basic") == PackIDSafe("acme__basic") {
		t.Fatal("distinct pack ids must not share a cache identity")
	}
	for _, invalid := range []string{`acme\basic`, "/acme/basic", "acme/basic/", " acme/basic"} {
		if err := ValidatePackID(invalid); err == nil {
			t.Fatalf("ValidatePackID(%q) accepted a non-canonical identity", invalid)
		}
	}
}
