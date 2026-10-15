package configlayout

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SiblingExecutable locates an executable shipped beside the host: the outer
// app's Contents/MacOS in the macOS bundle, the host's own directory elsewhere.
// It is "" when none is installed there.
func SiblingExecutable(name string) string {
	host, err := os.Executable()
	if err != nil {
		return ""
	}
	return siblingExecutable(host, name, runtime.GOOS)
}

func siblingExecutable(host, name, goos string) string {
	real, err := filepath.EvalSymlinks(host)
	if err != nil {
		real = host
	}
	if goos == "windows" {
		name += ".exe"
	}
	dir := filepath.Dir(real)
	if goos == "darwin" {
		if contents := MacOSAppContents(real); contents != "" {
			dir = filepath.Join(contents, "MacOS")
		}
	}
	path := filepath.Join(dir, name)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || (goos != "windows" && st.Mode()&0o111 == 0) {
		return ""
	}
	return path
}

// MacOSAppContents locates the outer app from its credential-owning helper,
// including launches through the installed CLI symlink.
func MacOSAppContents(executable string) string {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(real) {
		return ""
	}
	const suffix = "/Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw"
	real = filepath.ToSlash(real)
	if !strings.HasSuffix(real, suffix) {
		return ""
	}
	return filepath.FromSlash(strings.TrimSuffix(real, suffix) + "/Contents")
}

// LinuxInstallRoot is where the native package installs the host.
const LinuxInstallRoot = "/opt/painted-wolf-code"

// LinuxEngineRootDirName is the payload directory under the install root.
const LinuxEngineRootDirName = "engine-root"

func engineRootForExecutable(executable, goos string) string {
	switch goos {
	case "darwin":
		if contents := MacOSAppContents(executable); contents != "" {
			return filepath.Join(contents, "Resources", "engine-root")
		}
	case "linux":
		return linuxEngineRootForExecutable(executable, LinuxInstallRoot)
	}
	return ""
}

// linuxEngineRootForExecutable recognises a host launched from the package's
// bin directory, including through a symlink on PATH.
func linuxEngineRootForExecutable(executable, installRoot string) string {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(real) {
		return ""
	}
	binDir := filepath.Join(filepath.Clean(installRoot), "bin") + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(real), binDir) {
		return ""
	}
	return filepath.Join(filepath.Clean(installRoot), LinuxEngineRootDirName)
}
