package bundled

import (
	"debug/elf"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"strings"
)

type linuxImage struct {
	Path          string   `json:"path"`
	SHA256        string   `json:"sha256"`
	Architecture  string   `json:"architecture"`
	GLIBCVersions []string `json:"glibc_versions"`
	Dependencies  []string `json:"dependencies"`
	RPaths        []string `json:"rpaths"`
}

type linuxPlatformReport struct {
	SchemaVersion int          `json:"schema_version"`
	Platform      string       `json:"platform"`
	Architecture  string       `json:"architecture"`
	GLIBCMax      string       `json:"glibc_max"`
	Outer         linuxImage   `json:"outer"`
	Standalone    []linuxImage `json:"standalone"`
	Extracted     []linuxImage `json:"extracted"`
}

// linuxSystemLibrary reports a library every glibc host provides: glibc's own
// libraries, the loader, and libgcc_s, which glibc loads for thread cancellation
// and unwinding.
func linuxSystemLibrary(name string) bool {
	switch name {
	case "libc.so.6", "libm.so.6", "libdl.so.2", "libpthread.so.0", "librt.so.1",
		"libutil.so.1", "libresolv.so.2", "ld-linux-x86-64.so.2", "ld-linux-aarch64.so.1",
		"libgcc_s.so.1":
		return true
	}
	return false
}

func verifyLinuxPlatformReport(m *Manifest, proof *sourceProof) error {
	var checks linuxPlatformReport
	raw, err := readCandidateMetadata(filepath.Join(m.artifactDirectory, "platform-checks.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &checks); err != nil {
		return err
	}
	var lock struct {
		Linux struct {
			GLIBCMax string `json:"glibc_max"`
			Python   struct {
				SHA256 string `json:"sha256"`
			} `json:"python"`
		} `json:"linux"`
	}
	if err := sourceDocument(proof, "locks/runtimes.json", &lock); err != nil {
		return err
	}
	if lock.Linux.GLIBCMax != "2.35" || checks.GLIBCMax != lock.Linux.GLIBCMax || !validHex(lock.Linux.Python.SHA256, 32) {
		return fmt.Errorf("runtime lock for Linux differs from the supported glibc baseline")
	}
	var runtime struct {
		Python struct {
			SourceSHA256 string `json:"source_sha256"`
		} `json:"python_runtime"`
	}
	raw, err = readCandidateMetadata(filepath.Join(m.artifactDirectory, "runtime.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &runtime); err != nil {
		return err
	}
	if runtime.Python.SourceSHA256 != lock.Linux.Python.SHA256 {
		return fmt.Errorf("runtime for Linux Python differs from locked source")
	}
	if err := validateLinuxPlatformReport(checks, m.identity.GOARCH, m.identity.BinarySHA256); err != nil {
		return err
	}
	return verifyLinuxExecutable(filepath.Join(m.artifactDirectory, "opengrep"), m.identity.GOARCH)
}

func verifyLinuxExecutable(filename, arch string) error {
	image, err := elf.Open(filename)
	if err != nil {
		return fmt.Errorf("read Linux executable: %w", err)
	}
	defer func() { _ = image.Close() }()
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	if image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB || image.Machine != machine {
		return fmt.Errorf("executable architecture for Linux differs from qualification")
	}
	return nil
}

func validateLinuxPlatformReport(checks linuxPlatformReport, arch, binaryHash string) error {
	if checks.SchemaVersion != 1 || checks.Platform != "linux" || checks.Architecture != arch || checks.GLIBCMax != "2.35" {
		return fmt.Errorf("platform qualification identity for Linux differs")
	}
	if checks.Outer.Path != "opengrep" || checks.Outer.SHA256 != binaryHash || len(checks.Standalone) == 0 || !reflect.DeepEqual(checks.Standalone, checks.Extracted) {
		return fmt.Errorf("platform qualification for Linux does not bind the packaged and extracted images")
	}
	if len(checks.Outer.RPaths) != 0 {
		return fmt.Errorf("launcher for Linux has runtime search paths")
	}
	for _, library := range checks.Outer.Dependencies {
		if !linuxSystemLibrary(library) {
			return fmt.Errorf("launcher for Linux requires external library %s", library)
		}
	}
	if err := validateLinuxImage(checks.Outer, arch); err != nil {
		return err
	}
	images := make(map[string]bool)
	for _, image := range checks.Standalone {
		if images[image.Path] {
			return fmt.Errorf("duplicate Linux image %s", image.Path)
		}
		if err := validateLinuxImage(image, arch); err != nil {
			return err
		}
		images[image.Path] = true
	}
	if !images["opengrep.bin"] || !images["semgrep/bin/opengrep-core"] {
		return fmt.Errorf("required Linux engine images are missing")
	}
	for _, image := range checks.Standalone {
		if err := validateLinuxLibraries(image, images); err != nil {
			return err
		}
	}
	return nil
}

func validateLinuxImage(image linuxImage, arch string) error {
	if !validArchivePath(image.Path) || !validHex(image.SHA256, 32) || image.Architecture != arch {
		return fmt.Errorf("invalid Linux image identity: %s", image.Path)
	}
	for _, version := range image.GLIBCVersions {
		if !supportedDeploymentTarget(version, 2, 35) {
			return fmt.Errorf("image for Linux exceeds glibc baseline: %s", image.Path)
		}
	}
	for _, library := range image.Dependencies {
		if library == "" || library == "." || library == ".." || strings.ContainsAny(library, "/\\") {
			return fmt.Errorf("invalid Linux dependency: %s", image.Path)
		}
	}
	for _, value := range image.RPaths {
		if value != "$ORIGIN" && !strings.HasPrefix(value, "$ORIGIN/") {
			return fmt.Errorf("image for Linux has a build-host runpath: %s", image.Path)
		}
		resolved := path.Join(path.Dir(image.Path), strings.TrimPrefix(value, "$ORIGIN/"))
		if value == "$ORIGIN" {
			resolved = path.Dir(image.Path)
		}
		if resolved == ".." || strings.HasPrefix(resolved, "../") {
			return fmt.Errorf("runpath for Linux escapes its distribution: %s", image.Path)
		}
	}
	return nil
}

func validateLinuxLibraries(image linuxImage, images map[string]bool) error {
	var search []string
	for _, value := range image.RPaths {
		search = append(search, path.Join(path.Dir(image.Path), strings.TrimPrefix(strings.TrimPrefix(value, "$ORIGIN"), "/")))
	}
	for _, library := range image.Dependencies {
		if linuxSystemLibrary(library) {
			continue
		}
		found := false
		for _, directory := range search {
			found = found || images[path.Join(directory, library)]
		}
		if !found {
			return fmt.Errorf("image for Linux %s lacks packaged dependency %s", image.Path, library)
		}
	}
	return nil
}
