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
	"testing"
)

func fixture(t *testing.T, content string) (*os.Root, []byte) {
	t.Helper()
	path := t.TempDir()
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

func TestMaterializeRetainsWholeOldRuntimeAcrossSourceReplacement(t *testing.T) {
	source, raw := fixture(t, "old resource")
	base := t.TempDir()
	store, locks := filepath.Join(base, "generations"), filepath.Join(base, "locks")
	owner := uint32(os.Geteuid())
	old, err := materialize(source, raw, store, locks, owner)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := openLifetime(locks, old.ManifestSHA256, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	current, err := materialize(source, raw, store, locks, owner)
	if err != nil || current != old {
		t.Fatalf("live identical reinstall=%+v,%v", current, err)
	}
	nextSource, nextRaw := fixture(t, "new resource")
	next, err := materialize(nextSource, nextRaw, store, locks, owner)
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
	if removed, err := reclaim(old.ManifestSHA256, func() (string, error) { return next.Root, nil }, store, locks, owner); removed || err != nil {
		t.Fatalf("reclaimed live runtime=%v,%v", removed, err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if removed, err := reclaim(old.ManifestSHA256, func() (string, error) { return old.Root, nil }, store, locks, owner); removed || err != nil {
		t.Fatalf("reclaimed selected runtime=%v,%v", removed, err)
	}
	if removed, err := reclaim(old.ManifestSHA256, func() (string, error) { return next.Root, nil }, store, locks, owner); !removed || err != nil {
		t.Fatalf("reclaim unselected runtime=%v,%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(locks, old.ManifestSHA256+".lock")); err != nil {
		t.Fatal("lifetime lock inode was removed", err)
	}
}

func TestSelectionFailureAndSourceTamperNeverAdvanceOrRewriteRetainedBytes(t *testing.T) {
	source, raw := fixture(t, "resource")
	base := t.TempDir()
	store, locks := filepath.Join(base, "generations"), filepath.Join(base, "locks")
	owner := uint32(os.Geteuid())
	generation, err := materialize(source, raw, store, locks, owner)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("policy commit refused")
	if err := selectGeneration(generation.ManifestSHA256, func(Generation) error { return refused }, store, locks, owner); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if err := source.WriteFile("resource.data", []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation.Root, "resource.data"), []byte("corrupt retained"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := materialize(source, raw, store, locks, owner); err == nil {
		t.Fatal("reinstall silently replaced corrupt retained generation")
	}
	content, _ := os.ReadFile(filepath.Join(generation.Root, "resource.data"))
	if string(content) != "corrupt retained" {
		t.Fatal("retained bytes were rewritten")
	}
	called := false
	if err := selectGeneration(generation.ManifestSHA256, func(Generation) error { called = true; return nil }, store, locks, owner); err == nil || called {
		t.Fatal("unverified generation reached policy writer")
	}
}
