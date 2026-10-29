package git

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/repochange"
)

// networkCommands are the remote subcommands NetworkCommand runs.
var networkCommands = map[string]bool{"fetch": true, "push": true, "ls-remote": true}

// NetworkCommandTimeout bounds one network command.
const NetworkCommandTimeout = 10 * time.Minute

// RemoteURL names the remote a network argv contacts in dir.
func (m *Manager) RemoteURL(ctx context.Context, dir string, args []string) string {
	return gitexec.RemoteURLFor(ctx, dir, args)
}

// NetworkCommand runs fetch, push or ls-remote with the host's credential
// helper and proxy policy, streaming Git's stdout. fetch and push hold the
// repository lease; none of them touch the work tree or index.
func (m *Manager) NetworkCommand(ctx context.Context, dir string, args []string, stdout io.Writer) ([]byte, int, error) {
	if len(args) == 0 || !networkCommands[args[0]] {
		return nil, -1, fmt.Errorf("git: %q is not a network command", firstArg(args))
	}
	if args[0] != "ls-remote" {
		release, err := gitlease.Repository(ctx, dir)
		if err != nil {
			return nil, -1, err
		}
		defer release()
	}
	opts := networkOpts(m.RemoteURL(ctx, dir, args), NetworkCommandTimeout)
	opts.ExtraConfig = []string{"color.ui=false", "fetch.recurseSubmodules=false", "submodule.recurse=false"}
	diagnostics, code, err := gitexec.RunTo(ctx, dir, args, opts, stdout)
	if err == nil && args[0] == "fetch" {
		notifyRepoChange(ctx, dir, repochange.HeadMoved)
	}
	return diagnostics, code, err
}

// CloneCommand clones url into destDir with admitted clone flags, holding the
// source and destination leases and claiming an exclusive destination directory.
func CloneCommand(ctx context.Context, url, destDir string, flags []string, stdout io.Writer) ([]byte, int, error) {
	if err := gitargv.ValidateCloneURL(url); err != nil {
		return nil, -1, err
	}
	parent, err := absProjectDir(filepath.Dir(destDir))
	if err != nil {
		return nil, -1, err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, -1, fmt.Errorf("resolve clone parent: %w", err)
	}
	destDir = filepath.Join(parent, filepath.Base(destDir))
	releaseSource, err := gitlease.CloneSource(ctx, url)
	if err != nil {
		return nil, -1, err
	}
	defer releaseSource()
	release, err := gitlease.Path(ctx, destDir)
	if err != nil {
		return nil, -1, err
	}
	defer release()
	owned, err := claimCloneDestination(destDir)
	if err != nil {
		return nil, -1, err
	}
	args := append(append([]string{"clone"}, flags...), "--", url, destDir)
	opts := networkOpts(url, NetworkCommandTimeout)
	opts.ExtraConfig = []string{"color.ui=false", "fetch.recurseSubmodules=false", "submodule.recurse=false"}
	diagnostics, code, err := gitexec.RunTo(ctx, parent, args, opts, stdout)
	if err != nil {
		return diagnostics, code, owned.failed(err)
	}
	if code != 0 {
		_ = owned.failed(fmt.Errorf("git clone failed"))
		return diagnostics, code, nil
	}
	return diagnostics, 0, nil
}
