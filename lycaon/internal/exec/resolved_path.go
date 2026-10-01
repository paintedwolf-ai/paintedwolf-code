package exec

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// resolvedPathSource supplies the PATH every inheriting child starts from. Wiring
// injects it so this package does not depend on the catalog that resolves it.
var resolvedPathSource atomic.Pointer[func() string]

// SetResolvedPathSource installs the resolved user PATH. Called once during startup.
func SetResolvedPathSource(fn func() string) {
	if fn == nil {
		resolvedPathSource.Store(nil)
		return
	}
	resolvedPathSource.Store(&fn)
}

// ResolvedPathValue returns the resolved PATH, or "" when nothing has been wired.
// Empty means "keep the inherited PATH", not "use no PATH".
func ResolvedPathValue() string {
	fn := resolvedPathSource.Load()
	if fn == nil {
		return ""
	}
	return (*fn)()
}

// EffectivePathValue is the PATH a child launched now starts from: the resolved
// PATH once wiring has run, otherwise the inherited one.
func EffectivePathValue() string {
	if value := ResolvedPathValue(); value != "" {
		return value
	}
	return os.Getenv("PATH")
}

// ErrNotInPath reports that no directory of the searched PATH holds the executable.
var ErrNotInPath = errors.New("executable not found in PATH")

// LookPathIn finds an executable in pathValue rather than the process PATH,
// which can differ from the PATH children are given.
func LookPathIn(name, pathValue string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", ErrNotInPath
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		// A path with a separator is checked directly, including Windows PATHEXT.
		if found, err := osexec.LookPath(filepath.Join(dir, name)); err == nil {
			return found, nil
		}
	}
	return "", ErrNotInPath
}

// LookPath resolves a program the way a child launched now finds it: a name
// with a path separator is checked as written, and a bare name is searched on
// EffectivePathValue.
func LookPath(name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		return osexec.LookPath(name)
	}
	return LookPathIn(name, EffectivePathValue())
}
