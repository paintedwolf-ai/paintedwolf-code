package configlayout

import (
	"path/filepath"
	"strings"
)

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

func engineRootForExecutable(executable, goos string) string {
	if goos == "darwin" {
		if contents := MacOSAppContents(executable); contents != "" {
			return filepath.Join(contents, "Resources", "engine-root")
		}
	}
	return ""
}
