//go:build linux

package packagegeneration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, content string) (*os.Root, []byte) {
	t.Helper()
	path := t.TempDir()
	// t.TempDir honors the host umask; an installer stages with explicit modes.
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	image := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(image)
	data := []byte(content)
	dataSum := sha256.Sum256(data)
	for name, raw := range map[string][]byte{"bin/pw": image, "resource.data": data} {
		if err := os.WriteFile(filepath.Join(path, name), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(path, "bin/pw"), 0755); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2", "architecture": "x86_64", "install_root": "/opt/painted-wolf-code",
		"executables": []map[string]any{{"path": "/opt/painted-wolf-code/bin/pw", "size": len(image), "sha256": hex.EncodeToString(sum[:])}},
		"payload": []map[string]any{
			{"path": "bin", "kind": "directory", "mode": 0755, "size": nil, "sha256": nil, "target": nil},
			{"path": "bin/pw", "kind": "file", "mode": 0755, "size": len(image), "sha256": hex.EncodeToString(sum[:]), "target": nil},
			{"path": "resource.data", "kind": "file", "mode": 0644, "size": len(data), "sha256": hex.EncodeToString(dataSum[:]), "target": nil}}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "manifest.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, raw
}

// testLayout anchors trust at an explicitly moded private base; host umask and
// shared temporary ancestors are outside the fixture's control.
func testLayout(t *testing.T) layout {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	return layout{store: filepath.Join(base, "generations"), locks: filepath.Join(base, "locks"), anchor: base, owner: uint32(os.Geteuid())}
}

func TestMaterializeRetainsWholeOldRuntimeAcrossSourceReplacement(t *testing.T) {
	source, raw := fixture(t, "old resource")
	l := testLayout(t)
	old, err := l.materialize(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := l.openLifetime(old.ManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	current, err := l.materialize(source, raw)
	if err != nil || current != old {
		t.Fatalf("live identical reinstall=%+v,%v", current, err)
	}
	nextSource, nextRaw := fixture(t, "new resource")
	next, err := l.materialize(nextSource, nextRaw)
	if err != nil {
		t.Fatal(err)
	}
	if next.Root == old.Root {
		t.Fatal("resource-only change reused old generation")
	}
	if err := source.WriteFile("resource.data", []byte("source overwritten"), 0644); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(old.Root, "resource.data"))
	if err != nil || string(content) != "old resource" {
		t.Fatalf("old generation changed: %q,%v", content, err)
	}
	if removed, err := l.reclaim(old.ManifestSHA256, func() (string, error) { return next.Root, nil }); removed || err != nil {
		t.Fatalf("reclaimed live runtime=%v,%v", removed, err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if removed, err := l.reclaim(old.ManifestSHA256, func() (string, error) { return old.Root, nil }); removed || err != nil {
		t.Fatalf("reclaimed selected runtime=%v,%v", removed, err)
	}
	if removed, err := l.reclaim(old.ManifestSHA256, func() (string, error) { return next.Root, nil }); !removed || err != nil {
		t.Fatalf("reclaim unselected runtime=%v,%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(l.locks, old.ManifestSHA256+".lock")); err != nil {
		t.Fatal("lifetime lock inode was removed", err)
	}
}

func TestMaterializeRefusesWritableStagingRoot(t *testing.T) {
	source, raw := fixture(t, "resource")
	info, err := source.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Chmod(".", 0775); err != nil {
		t.Fatal(err)
	}
	l := testLayout(t)
	_, err = l.materialize(source, raw)
	if err == nil || !strings.Contains(err.Error(), "uncontrolled payload object") {
		t.Fatalf("group-writable staging root (was %v) accepted: %v", info.Mode(), err)
	}
	if _, err := os.Lstat(l.store); !os.IsNotExist(err) {
		t.Fatalf("refused staging created store: %v", err)
	}
}

func TestSelectionFailureAndSourceTamperNeverAdvanceOrRewriteRetainedBytes(t *testing.T) {
	source, raw := fixture(t, "resource")
	l := testLayout(t)
	generation, err := l.materialize(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("policy commit refused")
	if err := l.selectGeneration(generation.ManifestSHA256, func(Generation) error { return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if err := source.WriteFile("resource.data", []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation.Root, "resource.data"), []byte("corrupt retained"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.materialize(source, raw); err == nil {
		t.Fatal("reinstall silently replaced corrupt retained generation")
	}
	content, _ := os.ReadFile(filepath.Join(generation.Root, "resource.data"))
	if string(content) != "corrupt retained" {
		t.Fatal("retained bytes were rewritten")
	}
	called := false
	if err := l.selectGeneration(generation.ManifestSHA256, func(Generation) error { called = true; return nil }); err == nil || called {
		t.Fatal("unverified generation reached policy writer")
	}
}

func TestDamagedGenerationIsRepairedOnlyWhileNoRuntimeHoldsIt(t *testing.T) {
	source, raw := fixture(t, "resource")
	l := testLayout(t)
	generation, err := l.materialize(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(generation.Root, "resource.data")
	if err := os.WriteFile(damaged, []byte("corrupt retained"), 0644); err != nil {
		t.Fatal(err)
	}
	live, err := l.openLifetime(generation.ManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := unix.Flock(int(live.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	if _, err := l.materialize(source, raw); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("damaged generation in use was not refused: %v", err)
	}
	if content, _ := os.ReadFile(damaged); string(content) != "corrupt retained" {
		t.Fatal("bytes held by a live runtime were rewritten")
	}
	if err := unix.Flock(int(live.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	repaired, err := l.materialize(source, raw)
	if err != nil || repaired != generation {
		t.Fatalf("unused damaged generation was not repaired: %+v, %v", repaired, err)
	}
	if content, _ := os.ReadFile(damaged); string(content) != "resource" {
		t.Fatalf("repair left %q", content)
	}
	selected := false
	if err := l.selectGeneration(generation.ManifestSHA256, func(Generation) error { selected = true; return nil }); err != nil || !selected {
		t.Fatalf("repaired generation was not selectable: %v", err)
	}
}

func TestInterruptedInstallationIsDiscardedUnderTheIdentityLock(t *testing.T) {
	source, raw := fixture(t, "resource")
	l := testLayout(t)
	if err := os.MkdirAll(l.store, 0755); err != nil {
		t.Fatal(err)
	}
	identity := digest(raw)
	leftover := filepath.Join(l.store, ".install-"+identity)
	if err := os.MkdirAll(filepath.Join(leftover, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "bin", "pw"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	generation, err := l.materialize(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
		t.Fatalf("interrupted staging survived installation: %v", err)
	}
	if err := os.Mkdir(leftover, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation.Root, "resource.data"), []byte("corrupt retained"), 0644); err != nil {
		t.Fatal(err)
	}
	removed, err := l.reclaim(identity, func() (string, error) { return "", nil })
	if err != nil || !removed {
		t.Fatalf("unselected damaged generation was not reclaimed: %v, %v", removed, err)
	}
	for _, name := range []string{generation.Root, leftover} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("%s survived reclamation: %v", name, err)
		}
	}
}
