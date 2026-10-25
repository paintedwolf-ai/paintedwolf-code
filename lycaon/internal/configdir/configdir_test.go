package configdir_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestChannelDirNameProdByDefault(t *testing.T) {
	t.Setenv(configdir.EnvDev, "")
	t.Setenv(configdir.EnvConfigDir, "")
	if got := configdir.ChannelDirName(); got != configdir.DirNameProd {
		t.Fatalf("ChannelDirName() = %q want %q", got, configdir.DirNameProd)
	}
	if got := configdir.Label(); got != configdir.LabelProd() {
		t.Fatalf("Label() = %q want %q", got, configdir.LabelProd())
	}
}

func TestChannelDirNameDev(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvConfigDir, "")
	if got := configdir.ChannelDirName(); got != configdir.DirNameDev {
		t.Fatalf("ChannelDirName() = %q want %q", got, configdir.DirNameDev)
	}
	if got := configdir.Label(); got != "~/.config/"+configdir.DirNameDev {
		t.Fatalf("Label() = %q", got)
	}
}

func TestHarnessChannelRequiresDevelopmentAndExplicitHarness(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	if !configdir.IsHarnessChannel() {
		t.Fatal("explicit development harness was not enabled")
	}
	t.Setenv(configdir.EnvHarness, "")
	if configdir.IsHarnessChannel() {
		t.Fatal("development channel enabled harness controls without the harness flag")
	}
}

func TestUserConfigDirOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, override)
	t.Setenv(configdir.EnvDev, "1")
	got, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	if got != override {
		t.Fatalf("UserConfigDir() = %q want override %q", got, override)
	}
	// Channel label is independent of the override path.
	if got := configdir.Label(); got != "~/.config/"+configdir.DirNameDev {
		t.Fatalf("Label() with override = %q", got)
	}
}

func TestRootDoesNotCreateOrRestrictStorage(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "existing"}[exists], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if exists {
				testutil.FailErr(t, "create root", os.Mkdir(root, 0o755))
				testutil.FailErr(t, "set root mode", os.Chmod(root, 0o755))
			}
			t.Setenv(configdir.EnvConfigDir, root)
			got, err := configdir.Root()
			testutil.FailErr(t, "Root", err)
			if got != root {
				t.Fatalf("Root() = %q, want %q", got, root)
			}
			info, err := os.Stat(root)
			if !exists {
				if !os.IsNotExist(err) {
					t.Fatalf("root lookup changed missing storage: info=%v err=%v", info, err)
				}
			} else {
				testutil.FailErr(t, "stat root", err)
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
					t.Fatalf("root lookup changed mode to %o", info.Mode().Perm())
				}
			}
			created, err := configdir.UserConfigDir()
			testutil.FailErr(t, "UserConfigDir", err)
			if created != got {
				t.Fatalf("lookup %q and storage %q disagree", got, created)
			}
			assertOwnerOnly(t, created)
		})
	}
}

func TestRootTracksSelectedChannelAndHome(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, "")
	for _, dev := range []string{"", "1"} {
		t.Setenv(configdir.EnvDev, dev)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		root, err := configdir.Root()
		testutil.FailErr(t, "Root", err)
		want := filepath.Join(home, ".config", configdir.ChannelDirName())
		if root != want {
			t.Fatalf("Root() = %q, want %q", root, want)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("lookup changed home-derived storage: %v", err)
		}
	}
}

func TestUserConfigDirUsesSelectedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, "")
	t.Setenv(configdir.EnvDev, "")
	got, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	want := filepath.Join(home, ".config", configdir.DirNameProd)
	if got != want {
		t.Fatalf("UserConfigDir() = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	testutil.FailErr(t, "stat config dir", err)
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", got)
	}
	assertOwnerOnly(t, got)
}

func TestUserConfigDirTightensAnExistingOverrideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not the access control on Windows")
	}
	override := t.TempDir()
	testutil.FailErr(t, "loosen override dir", os.Chmod(override, 0o755))
	t.Setenv(configdir.EnvConfigDir, override)
	t.Setenv(configdir.EnvDev, "1")

	got, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	if got != override {
		t.Fatalf("UserConfigDir() = %q want %q", got, override)
	}
	assertOwnerOnly(t, got)
}

func assertOwnerOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(dir)
	testutil.FailErr(t, "stat config dir", err)
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config dir %q has mode %o want 700", dir, perm)
	}
}
