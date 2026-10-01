// Package gitexec invokes the bundled toolchain through one hardened boundary.
package gitexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// Profile selects hermetic vs network-capable invocation.
type Profile int

const (
	// ProfileHermetic covers local operations.
	ProfileHermetic Profile = iota
	// ProfileNetwork adds remote credentials.
	ProfileNetwork
)

// Identity supplies author and committer metadata.
type Identity struct {
	Name      string
	Email     string
	Timestamp time.Time
}

// Opts configures one gitexec.Run invocation.
type Opts struct {
	Profile  Profile
	Identity *Identity // commit and tag metadata
	// RemoteURL overrides the repository-derived credential target.
	RemoteURL   string
	Timeout     time.Duration // defaults to exec.DefaultGitTimeout
	MaxOutput   int64         // defaults to exec.DefaultMaxOutputBytes
	ExtraConfig []string      // key=value overrides
	// IndexFile selects a host-owned scratch index for preparing a reviewed change.
	IndexFile string
	// Input carries a host-built Git plumbing protocol, never shell text. It
	// streams to stdin, so its size follows the operation it describes.
	Input []byte
}

// HostConfig reads user-owned transport configuration.
type HostConfig interface {
	// HelperFor returns the configured credential helper.
	HelperFor(ctx context.Context, remoteURL string) string
	// SSHCommand returns the configured SSH command.
	SSHCommand(ctx context.Context) string
}

var (
	hostConfigMu sync.RWMutex
	hostConfig   HostConfig

	binaryPathFn    = gitengine.BinaryPath
	emptyHooksDirFn = gitengine.EmptyHooksDir
)

// SetHostConfig installs the process-wide resolver.
func SetHostConfig(h HostConfig) {
	hostConfigMu.Lock()
	hostConfig = h
	hostConfigMu.Unlock()
}

func getHostConfig() HostConfig {
	hostConfigMu.RLock()
	defer hostConfigMu.RUnlock()
	return hostConfig
}

func resolveSSHCommand(ctx context.Context) string {
	h := getHostConfig()
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.SSHCommand(ctx))
}

// SigningUnsupportedError reports unavailable commit or tag signing.
type SigningUnsupportedError struct {
	Detail string
}

func (e *SigningUnsupportedError) Error() string {
	if strings.TrimSpace(e.Detail) == "" {
		return "gitexec: signing unsupported"
	}
	return "gitexec: signing unsupported: " + e.Detail
}

// Code returns the structured reject code for tool/API mapping.
func (e *SigningUnsupportedError) Code() string { return "GIT_SIGNING_UNSUPPORTED" }

// Run invokes the bundled git with neutralization and hermetic env.
func Run(ctx context.Context, projectDir string, args []string, opts Opts) ([]byte, int, error) {
	return run(ctx, projectDir, args, opts, nil)
}

// RunTo streams stdout to a caller-owned sink and returns separate diagnostics.
func RunTo(ctx context.Context, projectDir string, args []string, opts Opts, stdout io.Writer) ([]byte, int, error) {
	if stdout == nil {
		return nil, -1, fmt.Errorf("git output sink is required")
	}
	return run(ctx, projectDir, args, opts, stdout)
}

func run(ctx context.Context, projectDir string, args []string, opts Opts, stdout io.Writer) ([]byte, int, error) {
	bin, err := binaryPathFn()
	if err != nil {
		return nil, -1, err
	}
	cwd, err := validateProjectDir(projectDir)
	if err != nil {
		return nil, -1, err
	}
	if err := auditRepoConfig(ctx, cwd); err != nil {
		return nil, -1, err
	}
	argv, err := buildArgv(ctx, cwd, opts, args)
	if err != nil {
		return nil, -1, err
	}
	env, err := buildEnv(ctx, bin, opts)
	if err != nil {
		return nil, -1, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = exec.DefaultGitTimeout
	}
	maxOut := int(opts.MaxOutput)
	if maxOut <= 0 {
		maxOut = exec.DefaultMaxOutputBytes
	}

	var stdin *exec.StdinSpec
	if len(opts.Input) > 0 {
		stdin = &exec.StdinSpec{Reader: io.NopCloser(bytes.NewReader(opts.Input))}
	}
	out, diagnostics, code, err := exec.RunSeparate(ctx, bin, argv, exec.ExecOpts{
		Launch:         exec.HostLaunch("git"),
		Dir:            cwd,
		Timeout:        timeout,
		MaxOutputBytes: maxOut,
		Stdout:         stdout,
		AppendEnv:      env,
		Stdin:          stdin,
	})
	if stdout != nil {
		out = diagnostics
	} else if code != 0 {
		out = append(out, diagnostics...)
	}
	if err != nil {
		return out, code, err
	}
	if code != 0 {
		if se := signingUnsupported(args, opts); se != nil {
			return out, code, se
		}
	}
	return out, code, nil
}

// validateProjectDir shares canonical spelling with repository discovery.
func validateProjectDir(projectDir string) (string, error) {
	if strings.TrimSpace(projectDir) == "" {
		return "", fmt.Errorf("gitexec: project dir is required")
	}
	root := gitrepo.CanonicalDir(projectDir)
	if root == "" {
		return "", fmt.Errorf("gitexec: project dir cannot be made absolute: %q", projectDir)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("gitexec: project dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("gitexec: project dir is not a directory")
	}
	return root, nil
}

func buildArgv(ctx context.Context, cwd string, opts Opts, args []string) ([]string, error) {
	prefix, err := neutralizeArgs(resolveSSHCommand(ctx))
	if err != nil {
		return nil, err
	}
	// Override order is security-sensitive.
	out := append([]string{}, prefix...)
	out = append(out, lfsFilterArgs()...)
	for _, kv := range opts.ExtraConfig {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		out = append(out, "-c", kv)
	}
	if opts.Profile == ProfileNetwork {
		if h := getHostConfig(); h != nil {
			remote := strings.TrimSpace(opts.RemoteURL)
			if remote == "" {
				remote = remoteURLFor(ctx, cwd, args)
			}
			if helper := strings.TrimSpace(h.HelperFor(ctx, remote)); helper != "" {
				resolved, err := resolveCredentialHelper(helper)
				if err != nil {
					return nil, err
				}
				out = append(out, "-c", "credential.helper="+resolved)
			}
		}
	}
	return append(out, diffDriverFlagArgs(args)...), nil
}

func gitExecPath(bin string) string {
	return gitExecPathForOS(bin, runtime.GOOS)
}

func gitExecPathForOS(bin, goos string) string {
	if goos == "windows" {
		// Windows stores helpers outside cmd.
		return filepath.Clean(filepath.Join(filepath.Dir(bin), "..", "mingw64", "libexec", "git-core"))
	}
	return filepath.Clean(filepath.Join(filepath.Dir(bin), "..", "libexec", "git-core"))
}
