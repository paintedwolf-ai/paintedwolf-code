package gitexec

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/gitcredential"
)

// hostHelper names the host's own credential-helper verb.
type hostHelper string

const (
	hostHelperNone     hostHelper = ""
	hostHelperKeychain hostHelper = "git-credential-osxkeychain"
	hostHelperVault    hostHelper = "git-credential-vault"
)

// resolveCredentialHelper translates the user's configured helper into the
// command Git runs. The host serves its own helpers; any other value stays the
// user's command.
func (e execution) resolveCredentialHelper(helper string) (string, hostHelper, error) {
	return e.resolveCredentialHelperFor(helper, runtime.GOOS)
}

func (e execution) resolveCredentialHelperFor(helper, goos string) (string, hostHelper, error) {
	helper = strings.TrimSpace(helper)
	selected := hostHelperNone
	switch {
	case helper == "osxkeychain":
		selected = hostHelperKeychain
	case goos == "linux" && (helper == "" || helper == "store"):
		// Unset means Git would prompt, which the host disables; `store` is a
		// plaintext file the host never writes.
		selected = hostHelperVault
	}
	if selected == hostHelperNone {
		return helper, selected, nil
	}
	executable, err := e.executablePath()
	if err != nil {
		return "", selected, fmt.Errorf("resolve built-in credential helper: %w", err)
	}
	// Shell snippets require the executable path as one quoted token.
	return "!" + shellQuote(executable) + " " + string(selected), selected, nil
}

// credentialService is the per-invocation vault service as gitexec uses it:
// its socket, binding to the started invocation, and release.
type credentialService struct {
	socket string
	bind   func(pid int) error
	stop   func() error
}

func startCredentialService(ctx context.Context, gitBinary string) (credentialService, error) {
	service, err := gitcredential.Serve(ctx, gitcredential.ServeOptions{GitBinary: gitBinary})
	if err != nil {
		return credentialService{}, err
	}
	return credentialService{socket: service.Path(), bind: service.Bind, stop: service.Close}, nil
}
