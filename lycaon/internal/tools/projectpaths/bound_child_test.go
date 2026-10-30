//go:build darwin || linux

package projectpaths

import (
	"fmt"
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

func TestFilteredDirectorySelectionBatchesUnselectedNames(t *testing.T) {
	directory := t.TempDir()
	for i := range 64 {
		testutil.FailErr(t, "seed hidden entry", os.WriteFile(filepath.Join(directory, fmt.Sprintf(".hidden-%02d", i)), nil, 0o600))
	}
	testutil.FailErr(t, "seed selected entry", os.WriteFile(filepath.Join(directory, "selected"), nil, 0o600))
	file, err := os.Open(directory)
	testutil.FailErr(t, "hold directory", err)
	defer func() { testutil.FailErr(t, "close directory", file.Close()) }()
	admissions := 0
	bound := BoundDir{file: file, dir: BoundPath{Resolved: Resolved{Abs: directory, ScopeRel: ".", DisplayPath: "."}, admitRead: func(string) error { admissions++; return nil }}}
	names, err := bound.Names(DirectoryNames{Maximum: 1})
	testutil.FailErr(t, "select visible membership", err)
	if len(names) != 1 || names[0] != "selected" || admissions > 2 {
		t.Fatalf("names=%v membership batches=%d", names, admissions)
	}
}
