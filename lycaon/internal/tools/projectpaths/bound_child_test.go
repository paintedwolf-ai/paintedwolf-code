//go:build darwin || linux

package projectpaths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHeldChildAdmissionUsesCanonicalDirectoryAddress(t *testing.T) {
	for _, directory := range []string{string(filepath.Separator), t.TempDir() + string(filepath.Separator)} {
		t.Run(directory, func(t *testing.T) {
			file, err := os.Open(directory)
			testutil.FailErr(t, "open named directory", err)
			defer func() { testutil.FailErr(t, "close directory", file.Close()) }()
			var admitted string
			bound := BoundDir{file: file, dir: BoundPath{Resolved: Resolved{Abs: directory, ScopeRel: ".", DisplayPath: "."}, admitEntry: func(actual string) error { admitted = actual; return nil }}}
			_, err = bound.Child("unobserved-name")
			testutil.FailErr(t, "admit immediate name", err)
			if want := filepath.Join(directory, "unobserved-name"); admitted != want {
				t.Fatalf("admitted=%q want=%q", admitted, want)
			}
		})
	}
}
