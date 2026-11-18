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
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitcredential"
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

// hostSSHCommand is the user's SSH command for host launches; an agent launch
// uses its confinement's SSH transport instead.
func (e execution) hostSSHCommand(ctx context.Context) string {
	if agentLaunched(ctx) {
		return ""
	}
	return e.resolveSSHCommand(ctx)
}

func (e execution) resolveSSHCommand(ctx context.Context) string {
	h := e.hostConfig
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
	return productionExecution().run(ctx, projectDir, args, opts, nil)
}

// RunTo streams stdout to a caller-owned sink and returns separate diagnostics.
func RunTo(ctx context.Context, projectDir string, args []string, opts Opts, stdout io.Writer) ([]byte, int, error) {
	if stdout == nil {
		return nil, -1, fmt.Errorf("git output sink is required")
	}
	return productionExecution().run(ctx, projectDir, args, opts, stdout)
}

func (e execution) run(ctx context.Context, projectDir string, args []string, opts Opts, stdout io.Writer) ([]byte, int, error) {
	bin, err := e.binaryPath()
	if err != nil {
		return nil, -1, err
	}
	cwd, err := validateProjectDir(projectDir)
	if err != nil {
		return nil, -1, err
	}
	launch, err := launchPlan(ctx, "git")
	if err != nil {
		return nil, -1, err
	}
	if err := e.auditRepoConfig(ctx, cwd); err != nil {
		return nil, -1, err
	}
	argv, network, err := e.buildArgv(ctx, cwd, opts, args)
	if err != nil {
		return nil, -1, err
	}
	env, err := e.buildEnv(ctx, bin, opts)
	if err != nil {
		return nil, -1, err
	}
	// An agent launch takes its proxy and SSH transport from its confinement.
	if opts.Profile == ProfileNetwork && launch.Kind != exec.LaunchAgentCommand {
		plan, err := planFor(e.proxyConfiguration(), network.remote)
		if err != nil {
			return nil, -1, err
		}
		env = append(env, plan.Environment()...)
	}
	var started func(pid int)
	if agent, ok := AgentLaunchFrom(ctx); ok && opts.Profile == ProfileNetwork && agent.Credentials != nil {
		env = append(env, gitcredential.SocketEnv+"="+agent.Credentials.Socket())
		// One network Git per command: a second launch cannot rebind the
		// service, so its helper requests fail closed.
		started = func(pid int) { _ = agent.Credentials.service.Bind(pid) }
	} else if network.helper == hostHelperVault {
		service, err := e.credentialService(ctx, bin)
		if err != nil {
			return nil, -1, fmt.Errorf("gitexec: start credential service: %w", err)
		}
		defer func() { _ = service.stop() }()
		env = append(env, gitcredential.SocketEnv+"="+service.socket)
		// An unbound service answers no helper, so a failed bind fails closed.
		started = func(pid int) { _ = service.bind(pid) }
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
		Launch:         launch,
		Dir:            cwd,
		Timeout:        timeout,
		MaxOutputBytes: maxOut,
		Stdout:         stdout,
		AppendEnv:      env,
		Stdin:          stdin,
		Started:        started,
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

// networkFacts are what a network invocation needs beyond its argv.
type networkFacts struct {
	// remote is the URL the invocation contacts, when it can be determined.
	remote string
	helper hostHelper
}

func (e execution) buildArgv(ctx context.Context, cwd string, opts Opts, args []string) ([]string, networkFacts, error) {
	var network networkFacts
	prefix, err := e.neutralizeArgs(e.hostSSHCommand(ctx))
	if err != nil {
		return nil, network, err
	}
	// Override order is security-sensitive.
	out := append([]string{}, prefix...)
	out = append(out, transportArgs(ctx)...)
	out = append(out, e.lfsFilterArgs()...)
	for _, kv := range opts.ExtraConfig {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		out = append(out, "-c", kv)
	}
	if opts.Profile == ProfileNetwork {
		network.remote = strings.TrimSpace(opts.RemoteURL)
		if network.remote == "" {
			network.remote = e.remoteURLFor(ctx, cwd, args)
		}
		if agent, ok := AgentLaunchFrom(ctx); ok {
			if agent.Credentials != nil {
				helper, err := e.executablePath()
				if err != nil {
					return nil, networkFacts{}, fmt.Errorf("resolve built-in credential helper: %w", err)
				}
				out = append(out, "-c", "credential.helper=!"+shellQuote(helper)+" "+string(hostHelperVault))
			}
		} else if h := e.hostConfig; h != nil {
			resolved, selected, err := e.resolveCredentialHelper(h.HelperFor(ctx, network.remote))
			if err != nil {
				return nil, networkFacts{}, err
			}
			if resolved != "" {
				out = append(out, "-c", "credential.helper="+resolved)
				network.helper = selected
			}
		}
	}
	return append(out, diffDriverFlagArgs(args)...), network, nil
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
