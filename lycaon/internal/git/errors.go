package git

import "errors"

// ErrNotRepository reports a path outside a worktree.
var ErrNotRepository = errors.New("not a git repository")
