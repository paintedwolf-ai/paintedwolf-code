//go:build linux

package driverenv

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
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
	if err := trustedLibrary(file, "/", 0); err == nil {
		t.Fatal("accepted writable or invalid library")
	}
	link := filepath.Join(root, "link.so")
	testutil.FailErr(t, "create library link", os.Symlink(file, link))
	if err := trustedLibrary(link, "/", 0); err == nil {
		t.Fatal("accepted unresolved library link")
	}
	if err := trustedLibrary(filepath.Join(root, "missing"), "/", 0); err == nil {
		t.Fatal("accepted missing library")
	}
}

func TestVersionedDriverLibraryResolution(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "usr/lib")
	testutil.FailErr(t, "create system library fixture", os.MkdirAll(directory, 0o755))
	version := "580.178.04"
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], 3)
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(header[18:], machine)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	binary.LittleEndian.PutUint16(header[54:], 56)
	binary.LittleEndian.PutUint16(header[58:], 64)
	names := []string{"libnvidia-glsi", "libnvidia-gpucomp", "libnvidia-eglcore", "libnvidia-glvkspirv", "libEGL_nvidia"}
	for _, name := range names {
		testutil.FailErr(t, "write versioned ELF fixture", os.WriteFile(filepath.Join(directory, name+".so."+version), header, 0o644))
	}
	paths, err := nvidiaLibraries(version, root, uint32(os.Getuid()))
	testutil.FailErr(t, "resolve matching driver components", err)
	if len(paths) != 5 || paths[4] != filepath.Join(directory, "libEGL_nvidia.so."+version) {
		t.Fatalf("driver paths = %v", paths)
	}
	if _, err := nvidiaLibraries("590.12", root, uint32(os.Getuid())); err == nil {
		t.Fatal("accepted mismatched driver version")
	}
	testutil.FailErr(t, "make library writable", os.Chmod(paths[0], 0o666))
	if _, err := nvidiaLibraries(version, root, uint32(os.Getuid())); err == nil {
		t.Fatal("accepted writable vendor library")
	}
	testutil.FailErr(t, "restore library mode", os.Chmod(paths[0], 0o644))
	header[18] = 0
	testutil.FailErr(t, "write incompatible ELF fixture", os.WriteFile(paths[0], header, 0o644))
	if _, err := nvidiaLibraries(version, root, uint32(os.Getuid())); err == nil {
		t.Fatal("accepted incompatible ELF machine")
	}
	if err := trustedLibrary("/outside/driver", root, uint32(os.Getuid())); err == nil {
		t.Fatal("accepted escaping library")
	}
}
