//go:build linux

package packagegeneration

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSystemExecutableAliasesRetainControlledPhysicalTargets(t *testing.T) {
	for _, name := range []string{"regular", "relative alias", "absolute alias", "directory alias", "parent alias",
		"loop", "relative escape", "absolute escape", "writable ancestor", "writable file", "not executable",
		"missing", "unclean", "relative path", "directory", "anchor", "file ancestor", "uncontrolled anchor"} {
		t.Run(name, func(t *testing.T) {
			l := testLayout(t)
			bin := filepath.Join(l.anchor, "bin")
			testutil.FailErr(t, "create system directory", os.Mkdir(bin, 0755))
			program := filepath.Join(bin, "program")
			testutil.FailErr(t, "create system executable", os.WriteFile(program, []byte("fixture"), 0755))
			path, target := program, ""
			switch name {
			case "relative alias":
				path, target = filepath.Join(l.anchor, "alias"), "bin/program"
			case "absolute alias":
				path, target = filepath.Join(l.anchor, "alias"), program
			case "directory alias":
				testutil.FailErr(t, "create directory alias", os.Symlink("bin", filepath.Join(l.anchor, "alias")))
				path = filepath.Join(l.anchor, "alias/program")
			case "parent alias":
				path, target = filepath.Join(bin, "alias"), "../bin/program"
			case "loop":
				path, target = filepath.Join(l.anchor, "alias"), "alias"
			case "relative escape":
				path, target = filepath.Join(l.anchor, "alias"), "../outside"
			case "absolute escape":
				path, target = filepath.Join(l.anchor, "alias"), filepath.Join(t.TempDir(), "outside")
			case "writable ancestor":
				testutil.FailErr(t, "make ancestor writable", os.Chmod(bin, 0777))
			case "writable file":
				testutil.FailErr(t, "make executable writable", os.Chmod(program, 0775))
			case "not executable":
				testutil.FailErr(t, "remove execute permission", os.Chmod(program, 0644))
			case "missing":
				path += ".missing"
			case "unclean":
				path = l.anchor + "/bin/../bin/program"
			case "relative path":
				path = "bin/program"
			case "directory":
				path = bin
			case "anchor":
				path = l.anchor
			case "file ancestor":
				path = filepath.Join(program, "child")
			case "uncontrolled anchor":
				testutil.FailErr(t, "make anchor writable", os.Chmod(l.anchor, 0777))
			}
			if target != "" {
				testutil.FailErr(t, "create executable alias", os.Symlink(target, path))
			}
			resolved, err := l.systemExecutable(path)
			accepted := name == "regular" || name == "relative alias" || name == "absolute alias" || name == "directory alias" || name == "parent alias"
			if accepted && (err != nil || resolved != program) || !accepted && err == nil {
				t.Fatalf("resolved %q: %v; want accepted %v and physical target %q", resolved, err, accepted, program)
			}
			if name == "loop" && !errors.Is(err, syscall.ELOOP) || name == "missing" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("resolver lost the structured filesystem error: %v", err)
			}
		})
	}
}

func TestInstallSelectsEachPhysicalHelperOnce(t *testing.T) {
	f := newInstallFixture(t)
	alias := filepath.Join(f.system, "x86_64-linux-gnu/WebKitGPUProcess")
	testutil.FailErr(t, "create system helper alias", os.Symlink("WebKitWebProcess", alias))
	policy := &recordedPolicy{}
	_, err := f.install(policy, "deb")
	testutil.FailErr(t, "install controlled aliases", err)
	if len(policy.access.Executables) != 2 || policy.access.Executables[0] == alias {
		t.Fatalf("policy retained aliases or duplicate physical helpers: %v", policy.access.Executables)
	}
}

func TestNativeSystemExecutableUsesRootTrust(t *testing.T) {
	resolved, err := ResolveSystemExecutable("/usr/bin/true")
	testutil.FailErr(t, "select installed system executable", err)
	want, err := filepath.EvalSymlinks("/usr/bin/true")
	testutil.FailErr(t, "read installed physical path", err)
	if resolved != want {
		t.Fatalf("physical executable = %q, want %q", resolved, want)
	}
}
