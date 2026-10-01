package gitexec

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

const identityQueryTimeout = 5 * time.Second

// ResolveIdentity returns a complete repository-over-global identity.
func ResolveIdentity(ctx context.Context, repoDir string) (Identity, bool) {
	bin, err := binaryPathFn()
	if err != nil {
		return Identity{}, false
	}
	prefix, err := neutralizeArgs(resolveSSHCommand(ctx))
	if err != nil {
		return Identity{}, false
	}
	globalPath := userGlobalGitConfigPath()
	if globalPath == "" {
		globalPath = "/dev/null"
	}

	queryCtx, cancel := context.WithTimeout(ctx, identityQueryTimeout)
	defer cancel()

	// Retain identity precedence while applying behavior hardening.
	out, code, err := exec.Run(queryCtx, bin, identityQueryArgv(prefix), exec.ExecOpts{
		Launch:         exec.HostLaunch("git_identity_query"),
		Dir:            repoDir,
		Timeout:        identityQueryTimeout,
		MaxOutputBytes: 64 << 10,
		AppendEnv: append([]string{
			"GIT_CONFIG_GLOBAL=" + globalPath,
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_PAGER=cat",
			"GIT_EXEC_PATH=" + gitExecPath(bin),
		}, bundledRuntimeEnv(bin)...),
	})
	if err != nil || code != 0 {
		return Identity{}, false
	}
	id := parseIdentity(out)
	if id.Name == "" || id.Email == "" {
		return Identity{}, false
	}
	return id, true
}

// identityQueryArgv reads both identity keys in one invocation.
func identityQueryArgv(prefix []string) []string {
	out := append([]string{}, prefix...)
	return append(out, "config", "--get-regexp", "^user\\.(name|email)$")
}

func parseIdentity(out []byte) Identity {
	var id Identity
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "user.name":
			id.Name = value
		case "user.email":
			id.Email = value
		}
	}
	return id
}
