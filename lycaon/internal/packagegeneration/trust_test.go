//go:build linux

package packagegeneration

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreAncestryIsControlledUpToAnchor(t *testing.T) {
	l := testLayout(t)
	if err := l.controlledParents(filepath.Join(l.store, "missing", "leaf")); err != nil {
		t.Fatalf("absent leaves under a controlled anchor refused: %v", err)
	}
	shared := filepath.Join(l.anchor, "shared")
	if err := os.Mkdir(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0775); err != nil {
		t.Fatal(err)
	}
	err := l.controlledParents(filepath.Join(shared, "generations"))
	if err == nil || !strings.Contains(err.Error(), "uncontrolled store ancestor") {
		t.Fatalf("group-writable ancestor accepted: %v", err)
	}
	if err := os.Chmod(l.anchor, 0775); err != nil {
		t.Fatal(err)
	}
	if err := l.controlledParents(l.store); err == nil {
		t.Fatal("writable anchor accepted")
	}
}

func TestStorePathMustStayBeneathAnchor(t *testing.T) {
	l := testLayout(t)
	for _, name := range []string{filepath.Dir(l.anchor), filepath.Join(filepath.Dir(l.anchor), "sibling"), "relative/store", l.anchor + "/../escape"} {
		if err := l.controlledParents(name); err == nil {
			t.Fatalf("store path %q outside anchor accepted", name)
		}
	}
	if native.anchor != "/" || native.owner != 0 || native.store != nativeStore || native.locks != lifetimeLocks {
		t.Fatalf("native layout must check the complete root-owned ancestry: %+v", native)
	}
}

func TestLifetimeSelectionAndMaterializeGuards(t *testing.T) {
	source, raw := fixture(t, "resource")
	l := testLayout(t)
	identity := digest(raw)
	if _, err := l.openLifetime("not-a-digest"); err == nil {
		t.Fatal("invalid identity accepted")
	}
	if err := os.MkdirAll(l.locks, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(l.locks, identity+".lock")
	if err := os.WriteFile(lockPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.openLifetime(identity); err == nil || !strings.Contains(err.Error(), "unsafe lifetime lock") {
		t.Fatalf("unsafe lifetime lock mode accepted: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := l.materialize(nil, raw); err == nil {
		t.Fatal("nil staging root accepted")
	}
	if _, err := l.materialize(source, []byte("{bad")); err == nil {
		t.Fatal("malformed manifest accepted")
	}
	otherRoot := bytes.ReplaceAll(raw, []byte(`"install_root":"/opt/painted-wolf-code"`), []byte(`"install_root":"/opt/other"`))
	if _, err := l.materialize(source, otherRoot); err == nil {
		t.Fatal("unexpected install_root accepted")
	}
	if err := l.selectGeneration(identity, nil); err == nil {
		t.Fatal("nil policy writer accepted")
	}
	if err := l.selectGeneration("bad", func(Generation) error { return nil }); err == nil {
		t.Fatal("invalid identity accepted by selectGeneration")
	}
	if _, err := l.reclaim(identity, nil); err == nil {
		t.Fatal("nil selection reader accepted")
	}
	if _, err := l.reclaim("bad", func() (string, error) { return "", nil }); err == nil {
		t.Fatal("invalid identity accepted by reclaim")
	}
	if _, err := l.reclaim(identity, func() (string, error) { return "", errors.New("reader failed") }); err == nil {
		t.Fatal("selection reader error ignored")
	}
	if err := os.MkdirAll(l.store, 0755); err != nil {
		t.Fatal(err)
	}
	if removed, err := l.reclaim(identity, func() (string, error) { return "", nil }); removed || err != nil {
		t.Fatalf("absent generation reclaim=%v,%v", removed, err)
	}
	genPath := filepath.Join(l.store, identity)
	if err := os.WriteFile(genPath, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.reclaim(identity, func() (string, error) { return "", nil }); err == nil {
		t.Fatal("non-directory generation reclaimed")
	}
	generation, err := l.materialize(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation.Root, "manifest.json"), bytes.Repeat([]byte(" "), len(raw)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := l.selectGeneration(identity, func(Generation) error { return nil }); err == nil {
		t.Fatal("corrupt retained seal selected")
	}
	if _, err := l.materialize(source, raw); err != nil {
		t.Fatalf("corrupt retained seal was not repaired: %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := Materialize(source, raw); err == nil {
			t.Fatal("non-root Materialize accepted")
		}
		if err := Select(identity, func(Generation) error { return nil }); err == nil {
			t.Fatal("non-root Select accepted")
		}
		if _, err := Reclaim(identity, func() (string, error) { return "", nil }); err == nil {
			t.Fatal("non-root Reclaim accepted")
		}
	}
}
