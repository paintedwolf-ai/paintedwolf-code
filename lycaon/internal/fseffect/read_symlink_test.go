package fseffect_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func readAll(t *testing.T, root, rel string) (string, error) {
	t.Helper()
	f, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: rel})
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, readErr := io.ReadAll(f)
	testutil.FailErr(t, "read opened target", readErr)
	return string(b), nil
}

func TestOpenReadFollowsContainedLinks(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir docs", os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	testutil.FailErr(t, "seed AGENTS.md",
		os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("policy\n"), 0o600))
	testutil.FailErr(t, "seed docs/guide.md",
		os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte("guide\n"), 0o600))

	testutil.FailErr(t, "relative link", os.Symlink("AGENTS.md", filepath.Join(root, "policy-link.md")))
	testutil.FailErr(t, "link into subdir",
		os.Symlink("docs/guide.md", filepath.Join(root, "guide-link")))
	testutil.FailErr(t, "absolute link",
		os.Symlink(filepath.Join(root, "AGENTS.md"), filepath.Join(root, "abs-link")))
	testutil.FailErr(t, "link climbing back down",
		os.Symlink("../AGENTS.md", filepath.Join(root, "docs", "up-link")))
	testutil.FailErr(t, "directory link", os.Symlink("docs", filepath.Join(root, "docs-link")))
	testutil.FailErr(t, "chain tail", os.Symlink("AGENTS.md", filepath.Join(root, "hop-b")))
	testutil.FailErr(t, "chain head", os.Symlink("hop-b", filepath.Join(root, "hop-a")))

	for name, tc := range map[string]struct{ rel, want string }{
		"relative link":        {"policy-link.md", "policy\n"},
		"link into subdir":     {"guide-link", "guide\n"},
		"absolute in-root":     {"abs-link", "policy\n"},
		"dotdot back in root":  {"docs/up-link", "policy\n"},
		"intermediate dirlink": {"docs-link/guide.md", "guide\n"},
		"link chain":           {"hop-a", "policy\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readAll(t, root, tc.rel)
			testutil.FailErr(t, "OpenRead "+tc.rel, err)
			if got != tc.want {
				t.Fatalf("read %q = %q, want %q", tc.rel, got, tc.want)
			}
		})
	}
}

func TestOpenReadRefusesLinksLeavingTheRoot(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("secret\n"), 0o600))
	testutil.FailErr(t, "mkdir docs", os.MkdirAll(filepath.Join(root, "docs"), 0o755))

	testutil.FailErr(t, "absolute escape", os.Symlink(outside, filepath.Join(root, "abs-escape")))
	testutil.FailErr(t, "relative escape",
		os.Symlink(filepath.Join("..", filepath.Base(outsideDir), "secret.txt"),
			filepath.Join(root, "rel-escape")))
	testutil.FailErr(t, "directory escape",
		os.Symlink(outsideDir, filepath.Join(root, "dir-escape")))
	testutil.FailErr(t, "dotdot escape",
		os.Symlink("../../etc/hosts", filepath.Join(root, "docs", "climb")))

	for name, rel := range map[string]string{
		"absolute target outside":    "abs-escape",
		"relative target outside":    "rel-escape",
		"intermediate dir outside":   "dir-escape/secret.txt",
		"dotdot past the root":       "docs/climb",
		"dotdot in the request path": "docs/../../" + filepath.Base(outsideDir) + "/secret.txt",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readAll(t, root, rel)
			if err == nil {
				t.Fatalf("read %q escaped the root and returned %q", rel, got)
			}
			if got != "" {
				t.Fatalf("read %q leaked %q alongside its error", rel, got)
			}
		})
	}
}

func TestOpenReadRefusesLinkCycle(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "loop a", os.Symlink("loop-b", filepath.Join(root, "loop-a")))
	testutil.FailErr(t, "loop b", os.Symlink("loop-a", filepath.Join(root, "loop-b")))

	_, err := readAll(t, root, "loop-a")
	if !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("cyclic link error = %v, want ErrSymlink", err)
	}
}

func TestOpenReadDanglingLinkIsNotExist(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "dangling link", os.Symlink("gone.md", filepath.Join(root, "dangling")))

	_, err := readAll(t, root, "dangling")
	if !os.IsNotExist(err) {
		t.Fatalf("dangling link error = %v, want IsNotExist", err)
	}
}

func TestWritesStillRefuseContainedLinks(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed target",
		os.WriteFile(filepath.Join(root, "real.txt"), []byte("original\n"), 0o600))
	testutil.FailErr(t, "in-root link", os.Symlink("real.txt", filepath.Join(root, "alias.txt")))

	if _, err := fseffect.OpenWrite(fseffect.Location{Root: root, Rel: "alias.txt"}, false, 0o600); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("OpenWrite through an in-root link = %v, want ErrSymlink", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "real.txt"))
	testutil.FailErr(t, "read real target", err)
	if string(got) != "original\n" {
		t.Fatalf("write reached the link target: %q", got)
	}
}
