package bundleddriver

import (
	"fmt"
	"os"
	"path/filepath"
)

type opengrepRunFiles struct {
	runDir           string
	jsonPath         string
	logPath          string
	settingsPath     string
	versionCachePath string
}

func (f opengrepRunFiles) dir() string { return f.runDir }

func newOpengrepRunFiles() (files opengrepRunFiles, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "paintedwolf-opengrep-")
	if err != nil {
		return opengrepRunFiles{}, nil, fmt.Errorf("opengrep run directory: %w", err)
	}
	files.runDir = dir
	cleanup = func() { _ = os.RemoveAll(dir) }

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
