package configlayout

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInstalledMacOSHostFindsOuterPayloads(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "canonicalize fixture root", err)
	contents := filepath.Join(root, "Painted Wolf Code.app", "Contents")
	host := filepath.Join(contents, "Helpers", "Painted Wolf Code engine.app", "Contents", "MacOS", "pw")
	testutil.FailErr(t, "create helper directory", os.MkdirAll(filepath.Dir(host), 0o755))
	testutil.FailErr(t, "write helper fixture", os.WriteFile(host, []byte("fixture"), 0o755))
	paths := []string{host}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "pw")
		testutil.FailErr(t, "create installed CLI link", os.Symlink(host, link))
		paths = append(paths, link)
	}
	for _, path := range paths {
		if got := MacOSAppContents(path); got != contents {
			t.Errorf("app contents for %q = %q, want %q", path, got, contents)
		}
		want := filepath.Join(contents, "Resources", "engine-root")
		if got := engineRootForExecutable(path, "darwin"); got != want {
			t.Errorf("payloads for %q = %q, want %q", path, got, want)
		}
		for _, goos := range []string{"linux", "windows"} {
			if got := engineRootForExecutable(path, goos); got != "" {
				t.Errorf("%s interpreted a macOS host as a bundled install: %q", goos, got)
			}
		}
	}
}

func TestMacOSHostDoesNotInventAnOuterBundle(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"pw", "host-bundle/Painted Wolf Code engine.app/Contents/MacOS/pw",
		"App.app/Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw-other",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		testutil.FailErr(t, "create standalone directory", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write standalone fixture", os.WriteFile(path, []byte("fixture"), 0o755))
		if got := MacOSAppContents(path); got != "" {
			t.Errorf("invented outer bundle for %q: %q", path, got)
		}
	}
	if got := MacOSAppContents(filepath.Join(root, "missing")); got != "" {
		t.Errorf("invented outer bundle for missing executable: %q", got)
	}
}

func TestEngineRootExplicitPayloadWins(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvEngineRoot, root)
	if got := EngineRoot(); got != root {
		t.Fatalf("engine root = %q, want explicit payload %q", got, root)
	}
}
