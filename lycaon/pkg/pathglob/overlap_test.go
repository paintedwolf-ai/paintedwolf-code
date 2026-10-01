package pathglob_test

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

func TestOverlap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical paths", "internal/db", "internal/db", true},
		{"identical globs", "internal/**", "internal/**", true},
		{"glob covers file", "internal/**", "internal/db/store.go", true},
		{"file under glob, reversed", "internal/db/store.go", "internal/**", true},
		{"directory covers descendant", "internal/db", "internal/db/store.go", true},
		{"descendant under directory, reversed", "internal/db/store.go", "internal/db", true},
		{"sibling directories", "internal/db", "internal/api", false},
		{"sibling globs", "internal/db/**", "internal/api/**", false},
		{"disjoint trees", "internal/db/**", "lycaon-den/src/**", false},
		{"two globs sharing a literal prefix", "internal/**/*.go", "internal/db/**", true},
		{"two globs with disjoint prefixes", "internal/db/**/*.go", "web/api/**", false},
		{"root dot claims everything", ".", "internal/db/**", true},
		{"double star claims everything", "**", "lycaon-den/src/app.ts", true},
		{"root on the right", "internal/db", "**", true},
		{"empty pattern is root-shaped", "", "internal/db", true},
		{"sibling files", "internal/db/store.go", "internal/db/schema.go", false},
		{"prefix is not a path boundary", "internal/db", "internal/database", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := pathglob.Overlap(tc.a, tc.b); got != tc.want {
				t.Fatalf("Overlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := pathglob.Overlap(tc.b, tc.a); got != tc.want {
				t.Fatalf("Overlap is not symmetric: Overlap(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}
