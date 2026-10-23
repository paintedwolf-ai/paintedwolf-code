package confine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const exclusionProbe = "PW_WRITE_EXCLUSION_PROBE"

func TestWriteExclusionProbe(t *testing.T) {
	mode := os.Getenv(exclusionProbe)
	if mode == "" {
		return
	}
	root := os.Getenv("PW_WRITE_EXCLUSION_ROOT")
	protected := filepath.Join(root, "dir", "draft.txt")
	var err error
	switch mode {
	case "write":
		err = os.WriteFile(protected, []byte("bad"), 0o644)
	case "chmod":
		err = os.Chmod(protected, 0o777)
	case "unlink":
		err = os.Remove(protected)
	case "replace":
		err = os.Rename(filepath.Join(root, "sibling.txt"), protected)
	case "ancestor":
		err = os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "moved"))
	case "declared-unlink":
		err = os.Remove(filepath.Join(root, "declared.txt"))
	case "declared-replace":
		err = os.Rename(filepath.Join(root, "sibling.txt"), filepath.Join(root, "declared.txt"))
	case "symlink":
		err = os.WriteFile(filepath.Join(root, "alias.txt"), []byte("bad"), 0o644)
	case "hardlink":
		err = os.Link(protected, filepath.Join(root, "hardlink.txt"))
	case "missing":
		err = os.WriteFile(filepath.Join(root, "missing.txt"), []byte("bad"), 0o644)
	case "read":
		data, readErr := os.ReadFile(protected)
		if readErr != nil || string(data) != "saved\n" {
			os.Exit(4)
		}
		os.Exit(0)
	case "sibling":
		err = os.WriteFile(filepath.Join(root, "created.txt"), []byte("ok"), 0o644)
		if err == nil {
			os.Exit(0)
		}
		os.Exit(5)
	default:
		os.Exit(6)
	}
	if err == nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestWriteExclusionsEnforceSandboxAgainstIndirectMutations(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "dir", "draft.txt")
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Dir(protected), 0o755))
	testutil.FailErr(t, "seed protected", os.WriteFile(protected, []byte("saved\n"), 0o644))
	testutil.FailErr(t, "seed sibling", os.WriteFile(filepath.Join(root, "sibling.txt"), []byte("ok\n"), 0o644))
	testutil.FailErr(t, "seed alias", os.Symlink(protected, filepath.Join(root, "alias.txt")))
	profile, err := BuildSeatbeltProfile(Confinement{Roots: []string{root}, WriteDenyPaths: []string{protected}})
	testutil.FailErr(t, "profile", err)
	self, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	for _, mode := range []string{"read", "sibling", "write", "chmod", "unlink", "replace", "ancestor", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command("/usr/bin/sandbox-exec", "-p", profile, self, "-test.run=^TestWriteExclusionProbe$")
			cmd.Env = append(os.Environ(), exclusionProbe+"="+mode, "PW_WRITE_EXCLUSION_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sandbox probe %s: %v\n%s", mode, err, output)
			}
			data, err := os.ReadFile(protected)
			testutil.FailErr(t, "protected remains", err)
			if string(data) != "saved\n" {
				t.Fatalf("protected content=%q", data)
			}
		})
	}
}

func TestWriteExclusionsRefuseExistingHardlinkAliases(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "draft.txt")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte("saved\n"), 0o644))
	testutil.FailErr(t, "link", os.Link(path, filepath.Join(root, "alias.txt")))
	if _, err := BuildSeatbeltProfile(Confinement{Roots: []string{root}, WriteDenyPaths: []string{path}}); err == nil {
		t.Fatal("aliased file admitted without an inode-wide write exclusion")
	}
}

func TestWriteExclusionsProtectAbsentPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing.txt")
	profile, err := BuildSeatbeltProfile(Confinement{Roots: []string{root}, WriteDenyPaths: []string{path}})
	testutil.FailErr(t, "absent path profile", err)
	self, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	cmd := exec.Command("/usr/bin/sandbox-exec", "-p", profile, self, "-test.run=^TestWriteExclusionProbe$")
	cmd.Env = append(os.Environ(), exclusionProbe+"=missing", "PW_WRITE_EXCLUSION_ROOT="+root)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("absent path sandbox: %v\n%s", err, output)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("protected absent path was created: %v", err)
	}
}

func TestWriteExclusionsProtectDeclaredSymlinkNamespace(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "dir", "draft.txt")
	declared := filepath.Join(root, "declared.txt")
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Dir(protected), 0o755))
	testutil.FailErr(t, "seed protected", os.WriteFile(protected, []byte("saved\n"), 0o644))
	testutil.FailErr(t, "seed sibling", os.WriteFile(filepath.Join(root, "sibling.txt"), []byte("other\n"), 0o644))
	testutil.FailErr(t, "declare symlink", os.Symlink(protected, declared))
	profile, err := BuildSeatbeltProfile(Confinement{Roots: []string{root}, WriteDenyPaths: []string{declared}})
	testutil.FailErr(t, "profile", err)
	self, err := os.Executable()
	testutil.FailErr(t, "executable", err)
	for _, mode := range []string{"read", "sibling", "write", "declared-unlink", "declared-replace"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command("/usr/bin/sandbox-exec", "-p", profile, self, "-test.run=^TestWriteExclusionProbe$")
			cmd.Env = append(os.Environ(), exclusionProbe+"="+mode, "PW_WRITE_EXCLUSION_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sandbox probe %s: %v\n%s", mode, err, output)
			}
			target, err := os.Readlink(declared)
			if err != nil || target != protected {
				t.Fatalf("declared namespace changed: target=%q err=%v", target, err)
			}
			data, err := os.ReadFile(protected)
			testutil.FailErr(t, "protected remains", err)
			if string(data) != "saved\n" {
				t.Fatalf("protected changed: %q", data)
			}
		})
	}
}
