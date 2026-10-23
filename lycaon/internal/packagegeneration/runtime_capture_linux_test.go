//go:build linux

package packagegeneration

import (
	"os"
	"path/filepath"
	"testing"
)

func runtimeCaptureFixture(t *testing.T) (layout, string, *os.Root) {
	t.Helper()
	l := testLayout(t)
	source := filepath.Join(l.anchor, "system")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "module.so"), []byte("original module"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "module.so"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("module.so", filepath.Join(source, "alias.so")); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(l.anchor, "candidate")
	if err := os.Mkdir(candidate, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(candidate, 0700); err != nil {
		t.Fatal(err)
	}
	target, err := os.OpenRoot(candidate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return l, source, target
}

func TestRuntimeCaptureRetainsOriginalBytesAndClosedLinks(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	captured, err := l.captureRuntime([]RuntimeOrigin{{Kind: "native", SelectedRoot: source}}, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Sources) != 1 || captured.Bytes != int64(len("original module")) || len(captured.Aliases) != 1 {
		t.Fatalf("capture=%+v", captured)
	}
	input := captured.Sources[0]
	if input.Path != filepath.Join(source, "module.so") || input.Observation == nil || input.Origin.SelectedRoot != source {
		t.Fatalf("source=%+v", input)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	raw, err := target.ReadFile(runtimeViewPath(filepath.Join(source, "alias.so")))
	if err != nil || string(raw) != "original module" {
		t.Fatalf("held alias=%q,%v", raw, err)
	}
	info, err := target.Stat(input.CapturedPath)
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("retained mode=%v,%v", info, err)
	}
	files, err := (&RuntimeSeal{Files: captured.Files}).validateFiles()
	if err != nil {
		t.Fatal(err)
	}
	if err := (&RuntimeSeal{Aliases: captured.Aliases}).validateAliases(files); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCaptureRejectsForeignAuthorityAndEscapingLink(t *testing.T) {
	for _, kind := range []string{"nix", "", "caller"} {
		t.Run(kind, func(t *testing.T) {
			l, source, target := runtimeCaptureFixture(t)
			if _, err := l.captureRuntime([]RuntimeOrigin{{Kind: kind, SelectedRoot: source}}, target); err == nil {
				t.Fatal("accepted non-native authority")
			}
		})
	}
	l, source, target := runtimeCaptureFixture(t)
	if err := os.Symlink("../foreign.so", filepath.Join(source, "escape.so")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.anchor, "foreign.so"), []byte("foreign"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.captureRuntime([]RuntimeOrigin{{Kind: "native", SelectedRoot: source}}, target); err == nil {
		t.Fatal("accepted link outside selected set")
	}
}

func TestRuntimeCaptureDetectsWholeSetChangeAndOpenReplacement(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	origins := []RuntimeOrigin{{Kind: "native", SelectedRoot: source}}
	before, err := l.observeRuntime(origins)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "late.so"), []byte("new entry"), 0644); err != nil {
		t.Fatal(err)
	}
	after, err := l.observeRuntime(origins)
	if err != nil {
		t.Fatal(err)
	}
	if sameRuntimeSnapshot(before, after) {
		t.Fatal("missed new selected directory entry")
	}
	module := filepath.Join(source, "module.so")
	if err := os.Rename(module, module+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(module, []byte("original module"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.copyRuntimeInputs(before, target); err == nil {
		t.Fatal("accepted replaced source inode")
	}
}

func TestRuntimeCaptureRejectsWritableOrNonprivateCustody(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	if err := os.Chmod(filepath.Join(source, "module.so"), 0664); err != nil {
		t.Fatal(err)
	}
	origins := []RuntimeOrigin{{Kind: "native", SelectedRoot: source}}
	if _, err := l.captureRuntime(origins, target); err == nil {
		t.Fatal("accepted writable source")
	}
	if err := os.Chmod(filepath.Join(source, "module.so"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := target.Chmod(".", 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.captureRuntime(origins, target); err == nil {
		t.Fatal("accepted nonprivate candidate")
	}
}
