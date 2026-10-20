package projectroot

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolvePreservesExactOperandNames(t *testing.T) {
	root := RootRef{ID: "fixture-root", Label: "fixture", Path: t.TempDir(), IsPrimary: true}
	for _, name := range []string{" leading", "trailing ", " ", "line\nbreak", "Case", "case", "-option", string([]byte{'n', 0xff})} {
		t.Run(name, func(t *testing.T) {
			for _, operand := range []string{name, "@fixture/" + name, filepath.Join(root.Path, name)} {
				got, selected, err := ResolveAbs([]RootRef{root}, root.ID, operand)
				testutil.FailErr(t, "resolve exact operand", err)
				if got != filepath.Join(root.Path, name) || selected.ID != root.ID {
					t.Fatalf("resolve %q = %q / %q", operand, got, selected.ID)
				}
			}
		})
	}
}

func TestScratchAddressPreservesExactOperandNames(t *testing.T) {
	for _, name := range []string{" leading", "trailing ", "line\nbreak"} {
		got, ok := ScratchAddress("@scratch/" + name)
		if !ok || got != name {
			t.Fatalf("scratch operand %q = %q, %v", name, got, ok)
		}
	}
	if _, ok := ScratchAddress(" @scratch/file"); ok {
		t.Fatal("a leading space must remain part of an ordinary filename")
	}
}
