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

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
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
	if _, err := seal(committedRelease(t), securityPack, "security-survey", t.TempDir(), true); err == nil {
		t.Fatal("check accepted a missing archive")
	}
}

// A release path that leaves the archive is refused, not read.
func TestVerifyArchiveStaysInsideTheArchive(t *testing.T) {
	parent := t.TempDir()
	archive := filepath.Join(parent, "archive")
	testutil.FailErr(t, "create archive", os.Mkdir(archive, 0o750))
	testutil.FailErr(t, "write outside file", os.WriteFile(filepath.Join(parent, "outside.md"), []byte("outside\n"), 0o600))
	if err := verifyArchive(archive, map[string][]byte{"../outside.md": []byte("outside\n")}); err == nil {
		t.Fatal("verified a file outside the archive")
	}
}

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

// The command reads a tagged release through git and seals it into the
// checkout; --check then accepts the result, and a tag the repository lacks
// fails without writing.
func TestRunSealsATaggedReleaseFromGit(t *testing.T) {
	repo := t.TempDir()
	for rel, data := range committedRelease(t) {
		target := filepath.Join(repo, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir release", os.MkdirAll(filepath.Dir(target), 0o755))
		testutil.FailErr(t, "write release", os.WriteFile(target, data, 0o600))
	}
	gittest.InitCommit(t, repo, "release")
	gittest.Run(t, repo, "tag", "v1.0.0")
	args := []string{"painted-wolf/security-survey", "security-survey", "v1.0.0"}

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), repo, args, &stdout, &stderr); code != 0 {
		t.Fatalf("seal exit %d: %s", code, stderr.String())
	}
	sealed, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(securityPack), "archive", "security-survey", "1.0.0", sumsFile))
	testutil.FailErr(t, "read sealed ledger", err)
	committed, err := os.ReadFile(filepath.Join(securityArchive, sumsFile))
	testutil.FailErr(t, "read committed ledger", err)
	if !bytes.Equal(sealed, committed) {
		t.Fatalf("sealed ledger from git differs from the committed archive:\n%s", sealed)
	}
	if code := run(t.Context(), repo, append([]string{"--check"}, args...), &stdout, &stderr); code != 0 {
		t.Fatalf("check exit %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(t.Context(), repo, []string{"painted-wolf/security-survey", "security-survey", "v9.9.9"}, &stdout, &stderr); code != 1 || stderr.Len() == 0 {
		t.Fatalf("unknown tag exit %d: %s", code, stderr.String())
	}
	if code := run(t.Context(), repo, []string{"--check"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing arguments exit %d", code)
	}
}

func TestSealRefusesAReleaseMissingRenderedGuidance(t *testing.T) {
	release := committedRelease(t)
	delete(release, securityPack+"/guidance/coordinator-security-plan.md")
	if _, err := seal(release, securityPack, "security-survey", t.TempDir(), false); err == nil {
		t.Fatal("sealed a version whose rendered guidance the release lacks")
	}
}

func TestRunSealsManifestOnlyReleaseWithoutFeedbackDirectory(t *testing.T) {
	repo := t.TempDir()
	pack := "lycaon/config/packs/painted-wolf/bugbash"
	manifest, err := os.ReadFile("../../config/packs/painted-wolf/bugbash/archive/bugbash/1.0.0/workflow.yaml")
	testutil.FailErr(t, "read released bugbash", err)
	target := filepath.Join(repo, filepath.FromSlash(pack), "workflows", "bugbash", "workflow.yaml")
	testutil.FailErr(t, "create released workflow directory", os.MkdirAll(filepath.Dir(target), 0755))
	testutil.FailErr(t, "write released manifest", os.WriteFile(target, manifest, 0600))
	gittest.InitCommit(t, repo, "manifest-only release")
	gittest.Run(t, repo, "tag", "v1.0.1")
	args := []string{"painted-wolf/bugbash", "bugbash", "v1.0.1"}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), repo, args, &stdout, &stderr); code != 0 {
		t.Fatalf("seal manifest-only release exit %d: %s", code, stderr.String())
	}
	if code := run(t.Context(), repo, append([]string{"--check"}, args...), &stdout, &stderr); code != 0 {
		t.Fatalf("check manifest-only release exit %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(t.Context(), repo, []string{"--check", "painted-wolf/bugbash", "bugbash", "missing-tag"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown release tag exit %d: %s", code, stderr.String())
	}
}
