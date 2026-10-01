package gitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

const hostConfigQueryTimeout = 5 * time.Second

// hostConfigResolver reads only user-owned global configuration.
type hostConfigResolver struct{}

// HostConfigResolver returns the production HostConfig.
func HostConfigResolver() HostConfig { return hostConfigResolver{} }

func (hostConfigResolver) HelperFor(ctx context.Context, remoteURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return ""
	}
	out, ok := hostConfigQuery(ctx, "credential:"+remoteURL, func(cfgPath string) []string {
		return hostConfigQueryArgv(cfgPath, "--get-urlmatch", "credential", remoteURL)
	})
	if !ok {
		return ""
	}
	return parseCredentialHelper(out)
}

func (hostConfigResolver) SSHCommand(ctx context.Context) string {
	out, ok := hostConfigQuery(ctx, "core.sshCommand", func(cfgPath string) []string {
		return hostConfigQueryArgv(cfgPath, "--get", "core.sshCommand")
	})
	if !ok {
		return ""
	}
	return firstNonEmptyLine(out)
}

// hostConfigQueryArgv scopes reads to one named file.
func hostConfigQueryArgv(cfgPath string, args ...string) []string {
	return append([]string{"config", "--file", cfgPath}, args...)
}

// Content-keyed entries make configuration edits visible immediately.
var hostConfigCache sync.Map // string -> []byte

func hostConfigQuery(ctx context.Context, key string, argv func(cfgPath string) []string) ([]byte, bool) {
	cfgPath := userGlobalGitConfigPath()
	if cfgPath == "" {
		return nil, false
	}
	sum, err := fileFingerprint(cfgPath)
	if err != nil {
		return nil, false
	}
	cacheKey := cfgPath + "|" + sum + "|" + key
	if cached, ok := hostConfigCache.Load(cacheKey); ok {
		out, _ := cached.([]byte)
		return out, len(out) > 0
	}
	out, ok := runHostConfigQuery(ctx, cfgPath, argv)
	if !ok {
		out = nil
	}
	hostConfigCache.Store(cacheKey, out)
	return out, len(out) > 0
}

// fileFingerprint detects same-size rewrites.
func fileFingerprint(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// runHostConfigQuery reports unavailable or unset values as false.
func runHostConfigQuery(ctx context.Context, cfgPath string, argv func(cfgPath string) []string) ([]byte, bool) {
	bin, err := binaryPathFn()
	if err != nil {
		return nil, false
	}

	queryCtx, cancel := context.WithTimeout(ctx, hostConfigQueryTimeout)
	defer cancel()

	// An empty directory excludes repository-local configuration.
	dir, err := os.MkdirTemp("", "lycaon-gitcfg-*")
	if err != nil {
		return nil, false
	}
	defer func() { _ = os.RemoveAll(dir) }()

	out, code, err := exec.Run(queryCtx, bin, argv(cfgPath), exec.ExecOpts{
		Launch:         exec.HostLaunch("git_config_query"),
		Dir:            dir,
		Timeout:        hostConfigQueryTimeout,
		MaxOutputBytes: 64 << 10,
		AppendEnv: append([]string{
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_ASKPASS=",
			"SSH_ASKPASS=",
			"GIT_PAGER=cat",
			"GIT_EXEC_PATH=" + gitExecPath(bin),
		}, bundledRuntimeEnv(bin)...),
	})
	if err != nil || code != 0 {
		return nil, false
	}
	return out, true
}

func userGlobalGitConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("GIT_CONFIG_GLOBAL")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() { //nolint:gosec // G703 — user-configured absolute gitconfig path
			return p
		}
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		p := filepath.Join(xdg, "git", "config")
		if st, err := os.Stat(p); err == nil && !st.IsDir() { //nolint:gosec // G703 — XDG config path from the process environment
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".gitconfig")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func firstNonEmptyLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func parseCredentialHelper(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		const key = "credential.helper"
		if !strings.HasPrefix(lower, key) {
			continue
		}
		rest := line[len(key):]
		if rest == "" {
			continue
		}
		switch rest[0] {
		case '=', ' ':
			if v := strings.TrimSpace(rest[1:]); v != "" {
				return v
			}
		}
	}
	return ""
}
