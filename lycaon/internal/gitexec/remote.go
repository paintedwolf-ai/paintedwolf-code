package gitexec

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

const remoteQueryTimeout = 10 * time.Second

const defaultRemoteName = "origin"

// remoteURLFor derives the credential target from arguments and repository state.
func remoteURLFor(ctx context.Context, repoDir string, args []string) string {
	name := remoteNameFromArgs(args)
	if name == "" {
		name = upstreamRemoteName(ctx, repoDir)
	}
	if name == "" {
		name = defaultRemoteName
	}
	if looksLikeURL(name) {
		return name
	}
	out, ok := runRemoteQuery(ctx, repoDir, []string{"remote", "get-url", name})
	if !ok {
		return ""
	}
	return firstNonEmptyLine(out)
}

// remoteNameFromArgs reads positions defined by each command grammar.
func remoteNameFromArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "clone":
		for _, a := range args[1:] {
			if looksLikeURL(a) {
				return a
			}
		}
		return ""
	case "push", "pull", "fetch", "ls-remote":
	default:
		return ""
	}
	for _, a := range args[1:] {
		if a == "--" {
			return ""
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

func upstreamRemoteName(ctx context.Context, repoDir string) string {
	out, ok := runRemoteQuery(ctx, repoDir, []string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"})
	if !ok {
		return ""
	}
	upstream := firstNonEmptyLine(out)
	name, _, found := strings.Cut(upstream, "/")
	if !found {
		return ""
	}
	return strings.TrimSpace(name)
}

// looksLikeURL recognizes transport syntax in a structured argument.
func looksLikeURL(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	// SCP-style syntax places the colon before any slash.
	if i := strings.Index(s, ":"); i > 0 {
		return !strings.Contains(s[:i], "/")
	}
	return false
}

func runRemoteQuery(ctx context.Context, repoDir string, args []string) ([]byte, bool) {
	bin, err := binaryPathFn()
	if err != nil {
		return nil, false
	}
	prefix, err := neutralizeArgs(resolveSSHCommand(ctx))
	if err != nil {
		return nil, false
	}
	queryCtx, cancel := context.WithTimeout(ctx, remoteQueryTimeout)
	defer cancel()

	out, code, err := exec.Run(queryCtx, bin, append(append([]string{}, prefix...), args...), exec.ExecOpts{
		Launch:         exec.HostLaunch("git_remote_query"),
		Dir:            repoDir,
		Timeout:        remoteQueryTimeout,
		MaxOutputBytes: 64 << 10,
		AppendEnv: append([]string{
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_PAGER=cat",
			"GIT_EXEC_PATH=" + gitExecPath(bin),
		}, bundledRuntimeEnv(bin)...),
	})
	if err != nil || code != 0 {
		return nil, false
	}
	return out, true
}

// RemoteEntry describes a configured git remote.
type RemoteEntry struct {
	Name string
	URL  string
}

// ListRemotes queries git for configured remotes in repoDir.
func ListRemotes(ctx context.Context, repoDir string) ([]RemoteEntry, bool) {
	out, ok := runRemoteQuery(ctx, repoDir, []string{"remote", "-v"})
	if !ok {
		return nil, false
	}
	var remotes []RemoteEntry
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			name := fields[0]
			rawURL := fields[1]
			key := name + "\x00" + rawURL
			if !seen[key] {
				seen[key] = true
				remotes = append(remotes, RemoteEntry{Name: name, URL: rawURL})
			}
		}
	}
	return remotes, true
}

