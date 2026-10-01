package repochange

// Source identifies the producer of a repository change.
type Source string

const (
	// SourceMutation is an agent (or host) file mutation tool after a successful write.
	SourceMutation Source = "mutation"
	// SourceWatcher is the pruned in-process worktree watcher (external edits).
	SourceWatcher Source = "watcher"
	// SourceTTL is a StatusCache miss caused by TTL expiry (no prior Notify).
	SourceTTL Source = "ttl"
	// SourceGitHost marks Git operations and worker merges.
	SourceGitHost Source = "git_host"
)
