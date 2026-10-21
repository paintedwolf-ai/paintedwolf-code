//go:build linux

package packagegeneration

import (
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
