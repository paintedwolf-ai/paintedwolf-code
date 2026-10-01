package grammarpatch

import "testing"

func TestLookup(t *testing.T) {
	tests := []struct {
		name           string
		wantFile       string
		wantRegenerate bool
		wantOK         bool
	}{
		{name: "typescript", wantFile: "tree-sitter-typescript-import-type.patch", wantRegenerate: true, wantOK: true},
		{name: "TSX", wantFile: "tree-sitter-typescript-import-type.patch", wantRegenerate: true, wantOK: true},
		{name: " yaml ", wantFile: "tree-sitter-yaml-multiline-quoted-scalars.patch", wantOK: true},
		{name: "go"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := Lookup(test.name)
			if ok != test.wantOK {
				t.Fatalf("Lookup(%q) found = %t, want %t", test.name, ok, test.wantOK)
			}
			if !ok {
				return
			}
			if got.File != test.wantFile || got.RegenerateParser != test.wantRegenerate {
				t.Fatalf("Lookup(%q) = %+v", test.name, got)
			}
		})
	}
}
