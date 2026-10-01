package gitexec

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

var agentSocketResolver = defaultAgentSocket

// buildEnv returns final environment overrides.
func buildEnv(ctx context.Context, bin string, opts Opts) ([]string, error) {
	hooksDir, err := emptyHooksDirFn()
	if err != nil {
		return nil, err
	}
	extras := []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_PAGER=cat",
		"GIT_FLUSH=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_EXEC_PATH=" + gitExecPath(bin),
		// Reuse the empty hooks directory as an empty template source.
		"GIT_TEMPLATE_DIR=" + hooksDir,
	}
	if opts.IndexFile != "" {
		if !filepath.IsAbs(opts.IndexFile) || strings.ContainsRune(opts.IndexFile, 0) {
			return nil, fmt.Errorf("scratch index must be an absolute path")
		}
		extras = append(extras, "GIT_INDEX_FILE="+opts.IndexFile)
	}
	extras = append(extras, bundledRuntimeEnv(bin)...)
	if socket := agentSocketResolver(ctx); socket != "" {
		extras = append(extras, "SSH_AUTH_SOCK="+socket)
	}
	if opts.Identity != nil {
		name := strings.TrimSpace(opts.Identity.Name)
		email := strings.TrimSpace(opts.Identity.Email)
		if name == "" || email == "" {
			return nil, fmt.Errorf("gitexec: Identity requires Name and Email")
		}
		extras = append(extras,
			"GIT_AUTHOR_NAME="+name,
			"GIT_AUTHOR_EMAIL="+email,
			"GIT_COMMITTER_NAME="+name,
			"GIT_COMMITTER_EMAIL="+email,
		)
		if !opts.Identity.Timestamp.IsZero() {
			when := opts.Identity.Timestamp.UTC().Format(time.RFC3339)
			extras = append(extras, "GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when)
		}
	}
	return extras, nil
}

func bundledRuntimeEnv(bin string) []string {
	return bundledRuntimeEnvForOS(bin, runtime.GOOS, exec.EffectivePathValue())
}

func bundledRuntimeEnvForOS(bin, goos, pathValue string) []string {
	root := filepath.Clean(filepath.Join(filepath.Dir(bin), ".."))
	switch goos {
	case "linux":
		return []string{
			"PREFIX=" + root,
			"GIT_SSL_CAINFO=" + filepath.Join(root, "ssl", "cacert.pem"),
		}
	case "windows":
		return []string{"PATH=" + strings.Join([]string{
			filepath.Join(root, "mingw64", "bin"),
			filepath.Join(root, "usr", "bin"),
			pathValue,
		}, ";")}
	default:
		return nil
	}
}
