package git

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// readCommands are the subcommands ReadCommand runs: each reads the
// repository and its work tree and writes nothing but optional-lock-free
// output. Their write forms (branch NAME, tag NAME, remote add, config
// KEY VALUE, worktree add) are excluded by the caller's catalog binding.
var readCommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true,
	"branch": true, "tag": true, "rev-parse": true, "rev-list": true,
	"merge-base": true, "remote": true, "config": true, "stash": true,
	"worktree": true, "ls-files": true, "clean": true,
}

// ReadCommandTimeout bounds one read command.
const ReadCommandTimeout = 2 * time.Minute

// CommandReader is the stateless hermetic Git read door.
type CommandReader struct{}

// ReadCommand runs one catalog-bound read-only Git command in dir and streams
// Git's own stdout to stdout. It returns Git's diagnostics and exit status.
// The invocation is hermetic: hooks, pager, editor, external diff and
// textconv drivers, fsmonitor and global configuration are disabled, and
// optional locks are off; config is the caller's allowlisted -c overrides.
func (CommandReader) ReadCommand(ctx context.Context, dir string, args, config []string, stdout io.Writer) ([]byte, int, error) {
	if len(args) == 0 || !readCommands[args[0]] {
		return nil, -1, fmt.Errorf("git: %q is not a read command", firstArg(args))
	}
	if args[0] == "config" {
		local, err := configReadArgv(args)
		if err != nil {
			return nil, -1, err
		}
		args = local
	}
	opts := hermeticOpts(ReadCommandTimeout)
	opts.ExtraConfig = append([]string{"color.ui=false"}, config...)
	return gitexec.RunTo(ctx, dir, args, opts, stdout)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
