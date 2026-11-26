package bundleddriver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
)

type opengrepRunFiles struct {
	workspace        confine.ActionWorkspace
	cacheDir         string
	jsonPath         string
	logPath          string
	settingsPath     string
	versionCachePath string
}

func (f opengrepRunFiles) dir() string { return f.workspace.Path() }

func (f opengrepRunFiles) cache() string {
	if f.cacheDir != "" {
		return f.cacheDir
	}
	return f.dir()
}

func newOpengrepRunFiles() (files opengrepRunFiles, cleanup func(), err error) {
	workspace, err := confine.NewActionWorkspace()
	if err != nil {
		return opengrepRunFiles{}, nil, fmt.Errorf("opengrep run directory: %w", err)
	}
	files.workspace = workspace
	dir := workspace.Path()
	// The reaper removes the run directory if the host exits before this cleanup.
	cleanup = exec.TrackScratchDir(dir)
	files.cacheDir = resolveOpengrepCacheDir(dir)

	files.jsonPath = filepath.Join(dir, "report.json")
	f, err := os.OpenFile(files.jsonPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return opengrepRunFiles{}, nil, fmt.Errorf("opengrep temp json: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return opengrepRunFiles{}, nil, fmt.Errorf("opengrep temp json: %w", err)
	}
	files.logPath = filepath.Join(dir, "semgrep.log")
	files.settingsPath = filepath.Join(dir, "settings.yml")
	files.versionCachePath = filepath.Join(dir, "version-cache")
	return files, cleanup, nil
}

// resolveOpengrepCacheDir returns a persistent, content-addressed cache root
// for OpenGrep's onefile runtime extraction so scans reuse the unpacked engine
// instead of re-extracting it into every ephemeral per-scan action workspace.
// Falls back to the per-scan workspace when no valid writable cache root exists.
func resolveOpengrepCacheDir(fallback string) string {
	base := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return fallback
		}
		base = strings.TrimSpace(userCache)
	}
	if base == "" || !filepath.IsAbs(base) {
		return fallback
	}
	candidate := filepath.Join(filepath.Clean(base), "paintedwolf")
	if err := confine.ValidateGrantedWriteRoots([]string{candidate}); err != nil {
		return fallback
	}
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		return fallback
	}
	return candidate
}

func openOpenGrepJSONOutput(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("opengrep read json output: %w", err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("opengrep json output: path is a symlink")
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("opengrep json output: not a regular file")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opengrep read json output: %w", err)
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("opengrep stat opened json output: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(st, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("opengrep json output changed before open")
	}
	return f, nil
}
