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
	defer before.Close()
	if err := os.WriteFile(filepath.Join(source, "late.so"), []byte("new entry"), 0644); err != nil {
		t.Fatal(err)
	}
	after, err := l.observeRuntime(origins)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
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

func TestRuntimeCaptureRewritesControlledAlternativesChain(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	alternatives := filepath.Join(l.anchor, "etc", "alternatives")
	if err := os.MkdirAll(alternatives, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(source, "alias.so")
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(alternatives, "module.so")
	if err := os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}
	original := "../../system/module.so"
	if err := os.Symlink(original, external); err != nil {
		t.Fatal(err)
	}
	captured, err := l.captureRuntime([]RuntimeOrigin{{Kind: "native", SelectedRoot: source}}, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Links) != 2 {
		t.Fatalf("link provenance=%+v", captured.Links)
	}
	for _, link := range captured.Links {
		if link.ResolvedPath != filepath.Join(source, "module.so") || link.CapturedPath != runtimeViewPath(link.ResolvedPath) || link.Observation == nil {
			t.Fatalf("link provenance=%+v", link)
		}
		if link.Path == external && link.Target != original {
			t.Fatal("lost original relative alternatives target")
		}
		if link.Path == alias && link.Target != external {
			t.Fatal("lost original absolute alternatives target")
		}
	}
	if err := os.RemoveAll(filepath.Join(l.anchor, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	data, err := target.ReadFile(runtimeViewPath(alias))
	if err != nil || string(data) != "original module" {
		t.Fatalf("retained alternatives=%q,%v", data, err)
	}
	files, err := (&RuntimeSeal{Files: captured.Files}).validateFiles()
	if err != nil {
		t.Fatal(err)
	}
	seal := &RuntimeSeal{Links: captured.Links, Aliases: captured.Aliases}
	if err := seal.validateLinks(files); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCapturePinsObservedAncestorAndDetectsReplacement(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	origins := []RuntimeOrigin{{Kind: "native", SelectedRoot: source}}
	before, err := l.observeRuntime(origins)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	moved := source + "-old"
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "module.so"), []byte("replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("module.so", filepath.Join(source, "alias.so")); err != nil {
		t.Fatal(err)
	}
	captured, err := l.copyRuntimeInputs(before, target)
	if err != nil {
		t.Fatal(err)
	}
	data, err := target.ReadFile(captured.Sources[0].CapturedPath)
	if err != nil || string(data) != "original module" {
		t.Fatalf("copied unobserved replacement=%q,%v", data, err)
	}
	after, err := l.observeRuntime(origins)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if sameRuntimeSnapshot(before, after) {
		t.Fatal("accepted replaced selected ancestor")
	}
}

func TestRuntimeCaptureBoundsDirectoryBeforeCollectingIt(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	opened, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, err := readRuntimeChildren(opened, 2); err == nil {
		t.Fatal("collected directory beyond remaining ceiling")
	}
}

func TestRuntimeCaptureResolvesSymlinkBeforeFollowingDotDot(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	deep := filepath.Join(source, "other", "deep")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("lexical decoy"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "other", "payload"), []byte("kernel selection"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other/deep", filepath.Join(source, "dirlink")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(source, "ordered.so")
	original := "dirlink/../payload"
	if err := os.Symlink(original, alias); err != nil {
		t.Fatal(err)
	}
	opened, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "kernel selection" {
		t.Fatalf("unexpected kernel lookup=%q", opened)
	}
	captured, err := l.captureRuntime([]RuntimeOrigin{{Kind: "native", SelectedRoot: source}}, target)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := target.ReadFile(runtimeViewPath(alias))
	if err != nil || string(retained) != string(opened) {
		t.Fatalf("retained=%q kernel=%q err=%v", retained, opened, err)
	}
	for _, link := range captured.Links {
		if link.Path == alias && (link.Target != original || link.ResolvedPath != filepath.Join(source, "other", "payload")) {
			t.Fatalf("lost ordered provenance=%+v", link)
		}
		if link.Path == filepath.Join(source, "dirlink") && link.ResolvedPath != deep {
			t.Fatalf("directory link refers to caller suffix=%+v", link)
		}
	}
}
