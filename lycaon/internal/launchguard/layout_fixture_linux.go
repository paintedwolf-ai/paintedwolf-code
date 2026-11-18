//go:build linux && launchfixture

package launchguard

import (
	"os"
	"path/filepath"
)

// A fixture build reads its layout beside its own executable, so one link
// serves every root-owned fixture directory a test installs it into. Release
// and development builds never carry the launchfixture tag.
func init() {
	exe, err := os.Executable()
	if err != nil {
		panic("launch fixture: locate executable: " + err.Error())
	}
	dir := filepath.Dir(exe)
	policyPath = filepath.Join(dir, "launch.json")
	lockDirectory = filepath.Join(dir, "locks")
}
