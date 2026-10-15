package gitexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	trust, err := hostTrustEnv(runtime.GOOS, opts.Profile)
	if err != nil {
		return nil, err
	}
	extras = append(extras, trust...)
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
	if goos != "windows" {
		return nil
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(bin), ".."))
	return []string{"PATH=" + strings.Join([]string{
		filepath.Join(root, "mingw64", "bin"),
		filepath.Join(root, "usr", "bin"),
		pathValue,
	}, ";")}
}

// hostTrustEnv points the Linux toolchain, which carries no trust store and
// whose TLS library has no usable default, at the host's CA bundle. Only a
// network invocation fails without one.
func hostTrustEnv(goos string, profile Profile) ([]string, error) {
	return hostTrustEnvFrom(goos, profile, hostCABundles)
}

func hostTrustEnvFrom(goos string, profile Profile, candidates []string) ([]string, error) {
	if goos != "linux" {
		return nil, nil
	}
	if bundle, ok := firstReadableFile(candidates); ok {
		return []string{"GIT_SSL_CAINFO=" + bundle}, nil
	}
	if profile == ProfileNetwork {
		return nil, &CABundleUnavailableError{Searched: slices.Clone(candidates)}
	}
	return nil, nil
}

// hostCABundles is crypto/x509's Linux certificate file search order.
var hostCABundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

func firstReadableFile(candidates []string) (string, bool) {
	for _, path := range candidates {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		_ = file.Close()
		if err == nil && info.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}

// CABundleUnavailableError reports that no host trust store is readable, so
// the bundled Git cannot verify TLS peers.
type CABundleUnavailableError struct {
	Searched []string
}

func (e *CABundleUnavailableError) Error() string {
	return "gitexec: no readable CA certificate bundle for HTTPS remotes (searched " + strings.Join(e.Searched, ", ") + "); install the distribution's CA certificates package"
}
