//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGenerationPolicyRejectsUnboundSelection(t *testing.T) {
	valid := `{"version":1,"generation_root":"/opt/painted-wolf-code/generations/abc","manifest_sha256":"` + strings.Repeat("a", 64) + `","logical_install_root":"/opt/painted-wolf-code","library_roots":["/usr/lib"]}`
	if _, err := decodePolicy([]byte(valid)); err != nil {
		t.Fatalf("decode bound generation: %v", err)
	}
	cases := map[string]string{
		"unknown field":       strings.TrimSuffix(valid, "}") + `,"command":"/bin/sh"}`,
		"trailing document":   valid + `{}`,
		"relative generation": strings.Replace(valid, `"/opt/painted-wolf-code/generations/abc"`, `"relative"`, 1),
		"unclean generation":  strings.Replace(valid, `generations/abc`, `generations/../abc`, 1),
		"root generation":     strings.Replace(valid, `"/opt/painted-wolf-code/generations/abc"`, `"/"`, 1),
		"uppercase hash":      strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"short hash":          strings.Replace(valid, strings.Repeat("a", 64), "ab", 1),
		"future version":      strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"relative executable": strings.TrimSuffix(valid, "}") + `,"system_executables":["usr/libexec"]}`,
		"root executable":     strings.TrimSuffix(valid, "}") + `,"system_executables":["/"]}`,
		"no library roots":    strings.Replace(valid, `,"library_roots":["/usr/lib"]`, "", 1),
		"empty library roots": strings.Replace(valid, `["/usr/lib"]`, `[]`, 1),
		"relative library":    strings.Replace(valid, `["/usr/lib"]`, `["usr/lib"]`, 1),
		"root library":        strings.Replace(valid, `["/usr/lib"]`, `["/"]`, 1),
		"unclean library":     strings.Replace(valid, `["/usr/lib"]`, `["/usr/../home"]`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePolicy([]byte(raw)); err == nil {
				t.Fatal("accepted unbound generation")
			}
		})
	}
}

func TestTrustedPathRejectsRelativeAndWritableAncestors(t *testing.T) {
	if file, err := openRootControlled("relative", false); err == nil {
		_ = file.Close()
		t.Fatal("accepted relative authority")
	}
	// A caller-owned temporary directory is deliberately not an installation
	// authority, regardless of whether its eventual file exists.
	if file, err := openRootControlled(t.TempDir()+"/missing", false); err == nil {
		_ = file.Close()
		t.Fatal("accepted caller-owned authority")
	}
}

func ownedPrivateDirectory(info os.FileInfo) error {
	facts, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(facts.Uid) != os.Geteuid() || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("not a private owned directory")
	}
	return nil
}

func TestLockDirectoryIsCreatedUnderControlledAncestry(t *testing.T) {
	base := t.TempDir()
	testutil.FailErr(t, "seal base", os.Chmod(base, 0o755))
	handle, err := os.Open(base)
	testutil.FailErr(t, "open base", err)
	defer handle.Close()
	syscall.Umask(0o002)
	defer syscall.Umask(0o022)
	// The directory is absent after boot; the first launch creates it.
	dir, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory)
	testutil.FailErr(t, "create lock directory", err)
	_ = dir.Close()
	for _, rel := range []string{"run", "run/paintedwolf", "run/paintedwolf/generation-locks"} {
		info, err := os.Lstat(filepath.Join(base, rel))
		testutil.FailErr(t, "stat "+rel, err)
		if !info.IsDir() || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode %v, want directory 0755 despite umask", rel, info.Mode())
		}
	}
	// An existing directory is reused.
	again, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory)
	testutil.FailErr(t, "reuse lock directory", err)
	_ = again.Close()
	// A writable or replaced component is refused, never followed.
	testutil.FailErr(t, "loosen ancestor", os.Chmod(filepath.Join(base, "run/paintedwolf"), 0o777))
	if dir, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory); err == nil {
		_ = dir.Close()
		t.Fatal("accepted a writable ancestor")
	}
	testutil.FailErr(t, "restore ancestor", os.Chmod(filepath.Join(base, "run/paintedwolf"), 0o755))
	testutil.FailErr(t, "remove lock directory", os.Remove(filepath.Join(base, "run/paintedwolf/generation-locks")))
	testutil.FailErr(t, "plant link", os.Symlink(t.TempDir(), filepath.Join(base, "run/paintedwolf/generation-locks")))
	if dir, err := ensureControlledDirectory(handle, "/run/paintedwolf/generation-locks", ownedPrivateDirectory); err == nil {
		_ = dir.Close()
		t.Fatal("followed a planted link")
	}
	for _, path := range []string{"relative", "/run/../etc", "/"} {
		if dir, err := ensureControlledDirectory(handle, path, ownedPrivateDirectory); err == nil {
			_ = dir.Close()
			t.Fatalf("accepted %q", path)
		}
	}
}
