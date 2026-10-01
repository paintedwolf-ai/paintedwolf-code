package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/configlayout"
)

const logsSiblingName = "pw-logs"

// runLogs delegates to the separate log viewer.
func runLogs(args []string) error {
	path, err := resolveLogsSibling()
	if err != nil {
		return err
	}
	return execLogsSibling(path, args)
}

func resolveLogsSibling() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate engine binary: %w", err)
	}
	return logsSiblingBeside(exe)
}

// logsSiblingBeside resolves the logs command within the installed bundle.
func logsSiblingBeside(enginePath string) (string, error) {
	real, err := filepath.EvalSymlinks(enginePath)
	if err != nil {
		real = enginePath
	}
	name := logsSiblingName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	directory := filepath.Dir(real)
	if runtime.GOOS == "darwin" {
		if contents := configlayout.MacOSAppContents(real); contents != "" {
			directory = filepath.Join(contents, "MacOS")
		}
	}
	candidate := filepath.Join(directory, name)
	info, err := os.Stat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s not found beside %s — install from the app bundle (Contents/MacOS) or run ./task logs:tui from a checkout", name, filepath.Base(real))
		}
		return "", err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable", candidate)
	}
	if abs, err := exec.LookPath(candidate); err == nil {
		return abs, nil
	}
	return candidate, nil
}
