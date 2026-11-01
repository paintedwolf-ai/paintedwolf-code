//go:build linux

package driverenv

import (
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

var versionPattern = regexp.MustCompile(`^[0-9]{3}\.[0-9]{1,3}(\.[0-9]{1,3})?$`)

// Prepare returns child-only variables and cleanup to retain until the child exits.
func Prepare(root string) ([]string, func(), error) {
	noop := func() {}
	config, err := readConfiguration(root)
	if err != nil || config == nil {
		return nil, noop, err
	}
	if _, err := os.Stat("/run/opengl-driver"); err == nil {
		return nil, noop, nil
	}
	defaults := strings.Join(config.MesaICDs, ":")
	if explicit := os.Getenv("VK_DRIVER_FILES"); explicit != "" && explicit != defaults {
		return nil, noop, nil
	}
	file, err := os.Open("/sys/module/nvidia/version")
	if os.IsNotExist(err) {
		return environment(*config, nil)
	}
	if err != nil {
		return nil, noop, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 128))
	file.Close()
	if readErr != nil {
		return nil, noop, readErr
	}
	version, err := loadedVersion(raw)
	if err != nil {
		return nil, noop, err
	}
	libraries, err := nvidiaLibraries(version)
	if err != nil {
		return nil, noop, err
	}
	return environment(*config, libraries)
}

func loadedVersion(raw []byte) (string, error) {
	version := strings.TrimSpace(string(raw))
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("invalid loaded NVIDIA driver version")
	}
	major, _ := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if major < 580 {
		return "", fmt.Errorf("included inference requires NVIDIA driver 580 or newer")
	}
	return version, nil
}

func nvidiaLibraries(version string) ([]string, error) {
	var directories []string
	switch runtime.GOARCH {
	case "amd64":
		directories = []string{"/usr/lib/x86_64-linux-gnu", "/lib/x86_64-linux-gnu"}
	case "arm64":
		directories = []string{"/usr/lib/aarch64-linux-gnu", "/lib/aarch64-linux-gnu"}
	default:
		return nil, fmt.Errorf("unsupported NVIDIA userspace architecture")
	}
	directories = append(directories, "/usr/lib64", "/usr/lib", "/lib64", "/lib")
	// EGL supplies Vulkan without importing the host's X11 libraries.
	names := []string{"libnvidia-glsi", "libnvidia-gpucomp", "libnvidia-eglcore", "libnvidia-glvkspirv", "libEGL_nvidia"}
	var paths []string
	for _, name := range names {
		leaf := name + ".so." + version
		var selected string
		for _, directory := range directories {
			path, err := filepath.EvalSymlinks(filepath.Join(directory, leaf))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if filepath.Base(path) != leaf {
				return nil, fmt.Errorf("NVIDIA library version differs from loaded driver")
			}
			if err := trustedLibrary(path); err != nil {
				return nil, err
			}
			selected = path
			break
		}
		if selected == "" {
			return nil, fmt.Errorf("NVIDIA driver component is missing: %s", leaf)
		}
		paths = append(paths, selected)
	}
	return paths, nil
}

func trustedLibrary(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 ||
			(current == path && !info.Mode().IsRegular()) || (current != path && !info.IsDir()) {
			return fmt.Errorf("NVIDIA library path is not system-owned: %s", current)
		}
		if current == "/" {
			break
		}
	}
	library, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer library.Close()
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	if library.Class != elf.ELFCLASS64 || library.Machine != machine || library.Type != elf.ET_DYN {
		return fmt.Errorf("NVIDIA library has an incompatible ELF architecture")
	}
	return nil
}
