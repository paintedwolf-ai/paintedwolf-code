//go:build linux

package driverenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadedVersionIsExactAndSupported(t *testing.T) {
	for _, input := range []string{"", "570.1", "580.1/driver", "580.1:injected", "580", "580.1 suffix", "580.1\n590.2"} {
		if _, err := loadedVersion([]byte(input)); err == nil {
			t.Fatalf("accepted version %q", input)
		}
	}
	for _, input := range []string{"580.178.04", "590.12"} {
		got, err := loadedVersion([]byte(input + "\n"))
		testutil.FailErr(t, "parse loaded version", err)
		if got != input {
			t.Fatalf("version = %q", got)
		}
	}
}

func TestExplicitDriverSelectionDoesNotCreateVendorEnvironment(t *testing.T) {
	root := configFixture(t, `{"format_version":1,"mesa_icds":["/nix/mesa.json"]}`)
	t.Setenv("VK_DRIVER_FILES", "/custom/driver.json")
	values, cleanup, err := Prepare(root)
	testutil.FailErr(t, "preserve explicit driver selection", err)
	cleanup()
	if len(values) != 0 {
		t.Fatalf("overrode explicit selection: %v", values)
	}
}

func TestDriverTrustRejectsUserFilesAndLinks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "driver.so")
	testutil.FailErr(t, "write untrusted library", os.WriteFile(file, []byte("not ELF"), 0o666))
	if err := trustedLibrary(file); err == nil {
		t.Fatal("accepted writable or invalid library")
	}
	link := filepath.Join(root, "link.so")
	testutil.FailErr(t, "create library link", os.Symlink(file, link))
	if err := trustedLibrary(link); err == nil {
		t.Fatal("accepted unresolved library link")
	}
	if err := trustedLibrary(filepath.Join(root, "missing")); err == nil {
		t.Fatal("accepted missing library")
	}
}
