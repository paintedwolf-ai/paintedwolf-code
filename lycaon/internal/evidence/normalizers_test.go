package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestFileRegionNormalizer_windowsAndUNC(t *testing.T) {
	shape, ok := evidence.DefaultShapeRegistry().Lookup(evidence.ShapeFileRegion)
	if !ok {
		t.Fatal("file_region shape missing from registry")
	}
	norm := shape.Normalizer
	cases := []struct {
		in, want string
	}{
		{`C:\repo\src\main.go`, `c:/repo/src/main.go`},
		{`\\server\share\file.go`, `//server/share/file.go`},
		{`./internal/api/foo.go`, `internal/api/foo.go`},
	}
	for _, tc := range cases {
		if got := norm.Normalize(tc.in); got != tc.want {
			t.Fatalf("Normalize(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}
