package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// treeRelease serves a release from an in-memory tree of repo-relative paths.
type treeRelease map[string][]byte

func (r treeRelease) show(rel string) ([]byte, error) {
	data, ok := r[rel]
	if !ok {
		return nil, fmt.Errorf("%s not in release", rel)
	}
	return data, nil
}

func (r treeRelease) list(dir string) ([]string, error) {
	var names []string
	for rel := range r {
		if path.Dir(rel) == dir {
			names = append(names, path.Base(rel))
		}
	}
	sort.Strings(names)
	return names, nil
}

const (
	securityPack    = "lycaon/config/packs/painted-wolf/security-survey"
	securityArchive = "../../config/packs/painted-wolf/security-survey/archive/security-survey/1.0.0"
)

// committedRelease rebuilds the security-survey 1.0.0 release tree from its
// committed archive, plus guidance the workflow never rendered.
func committedRelease(t *testing.T) treeRelease {
	t.Helper()
	release := treeRelease{securityPack + "/guidance/unrendered.md": []byte("Not rendered by the workflow.\n")}
	err := filepath.WalkDir(securityArchive, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == sumsFile {
			return err
		}
		rel, err := filepath.Rel(securityArchive, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		releasePath := path.Join(securityPack, filepath.ToSlash(rel))
		if rel == "workflow.yaml" {
			releasePath = securityPack + "/workflows/security-survey/workflow.yaml"
		}
		release[releasePath] = data
		return nil
	})
	testutil.FailErr(t, "read committed archive", err)
	return release
}

// Resealing the released bytes reproduces the committed ledger exactly, and
// --check refuses an archive that drifted from the release.
func TestSealReproducesTheCommittedArchive(t *testing.T) {
	packDir := t.TempDir()
	_, err := seal(committedRelease(t), securityPack, "security-survey", packDir, false)
	testutil.FailErr(t, "seal", err)
	sealed := filepath.Join(packDir, "archive", "security-survey", "1.0.0")
	got, err := os.ReadFile(filepath.Join(sealed, sumsFile))
	testutil.FailErr(t, "read sealed ledger", err)
	want, err := os.ReadFile(filepath.Join(securityArchive, sumsFile))
	testutil.FailErr(t, "read committed ledger", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("sealed ledger differs from the committed archive:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(sealed, "guidance", "unrendered.md")); err == nil {
		t.Fatal("sealed guidance the workflow never renders")
	}

	if _, err := seal(committedRelease(t), securityPack, "security-survey", packDir, true); err != nil {
		t.Fatalf("check rejected an intact archive: %v", err)
	}
	testutil.FailErr(t, "alter sealed guidance", os.WriteFile(filepath.Join(sealed, "guidance", "coordinator-security-plan.md"), []byte("edited\n"), 0o600))
	if _, err := seal(committedRelease(t), securityPack, "security-survey", packDir, true); err == nil {
		t.Fatal("check accepted an archive that differs from the release")
	}
}

func TestSealRefusesAReleaseMissingRenderedGuidance(t *testing.T) {
	release := committedRelease(t)
	delete(release, securityPack+"/guidance/coordinator-security-plan.md")
	if _, err := seal(release, securityPack, "security-survey", t.TempDir(), false); err == nil {
		t.Fatal("sealed a version whose rendered guidance the release lacks")
	}
}
