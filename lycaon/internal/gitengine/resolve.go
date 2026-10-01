package gitengine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// versionOfFn isolates version probes in tests.
var versionOfFn = versionOf

func resolve() (bin string, lfs string, err error) {
	if path := strings.TrimSpace(os.Getenv(EnvGitBinary)); path != "" && testEnvArmed() {
		if isExecutable(path) {
			if err := assertPinnedVersion(path); err != nil {
				return "", "", err
			}
			return path, lfsForBinary(path, runtime.GOOS), nil
		}
	}
	bin = bundledBinaryPath()
	if isExecutable(bin) {
		if err := assertPinnedVersion(bin); err != nil {
			return "", "", err
		}
		return bin, lfsForBinary(bin, runtime.GOOS), nil
	}
	return "", "", &UnavailableError{Reason: "missing"}
}

func testEnvArmed() bool {
	return configdir.EnvTruthy(os.Getenv(EnvTest))
}

func bundledBinaryPath() string {
	root := configlayout.EngineRoot()
	if root == "" {
		return ""
	}
	gitRel, _ := engineBinaryPaths(runtime.GOOS)
	return filepath.Clean(filepath.Join(root, "gitengine", gitRel))
}

func engineBinaryPaths(goos string) (gitRel, lfsRel string) {
	if goos == "windows" {
		return filepath.Join("cmd", "git.exe"), filepath.Join("mingw64", "libexec", "git-core", "git-lfs.exe")
	}
	return filepath.Join("bin", "git"), filepath.Join("bin", "git-lfs")
}

func isExecutable(path string) bool {
	st, err := os.Stat(path) //nolint:gosec // G703 — test override or fixed bundle path
	if err != nil || st.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}

func lfsForBinary(gitBin, goos string) string {
	if goos == "windows" {
		root := filepath.Dir(filepath.Dir(gitBin))
		_, lfsRel := engineBinaryPaths(goos)
		return filepath.Join(root, lfsRel)
	}
	return filepath.Join(filepath.Dir(gitBin), "git-lfs")
}

func assertPinnedVersion(bin string) error {
	expected, err := loadPinnedReportedVersion()
	if err != nil {
		return err
	}
	found, err := versionOfFn(bin)
	if err != nil {
		return &UnavailableError{Reason: "missing", Expected: expected}
	}
	if found != expected {
		return &UnavailableError{
			Reason:   "version_mismatch",
			Expected: expected,
			Found:    found,
		}
	}
	return nil
}

func versionOf(bin string) (string, error) {
	cmd, cleanup, err := execpkg.PrepareCommand(context.Background(), bin, []string{"--version"}, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("git_version_probe"),
	})
	if err != nil {
		return "", err
	}
	defer cleanup()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git --version: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return parseGitVersion(stdout.String())
}

// parseGitVersion extracts the reported version token.
func parseGitVersion(out string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			tok := fields[i+1]
			if j := strings.IndexAny(tok, " \t("); j >= 0 {
				tok = tok[:j]
			}
			return tok, nil
		}
	}
	return "", fmt.Errorf("gitengine: could not parse version from %q", strings.TrimSpace(out))
}
