// Package git provides structured repository operations.
package git

import (
	"context"
	"time"
)

// GitStatusEntry is one changed path from porcelain status.
type GitStatusEntry struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// GitStatus is a snapshot for board git slice and git_status tool.
type GitStatus struct {
	Branch        string           `json:"branch"`
	HeadShort     string           `json:"head_short,omitempty"`
	Upstream      string           `json:"upstream,omitempty"`
	Ahead         int              `json:"ahead"`
	Behind        int              `json:"behind"`
	Dirty         bool             `json:"dirty"`
	StagedCount   int              `json:"staged_count"`
	UnstagedCount int              `json:"unstaged_count"`
	Files         []GitStatusEntry `json:"files,omitempty"`
	RecentCommits []string         `json:"recent_commits,omitempty"`
}

// GitDiffStatEntry summarizes line changes for one path. Untracked marks a
// file git diff itself would not list, included on request as an addition.
type GitDiffStatEntry struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Untracked  bool   `json:"untracked,omitempty"`
}

// GitCommit is one entry from git_log.
type GitCommit struct {
	Hash    string    `json:"hash"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// GitLogOpts configures git log queries.
type GitLogOpts struct {
	Limit  int
	Offset int
	Ref    string
	Path   string
	All    bool
}

// GitDiffOpts configures git diff queries. Staged compares the index against
// HEAD; BaseRef replaces the implied side (the index, or HEAD when Staged)
// with the named ref.
type GitDiffOpts struct {
	Paths   []string
	Staged  bool
	BaseRef string
	HeadRef string
}

// GitRestoreOpts configures which Git tree receives content and from where.
// With no mode fields, Git's ordinary default is worktree from index.
type GitRestoreOpts struct {
	Review   func(context.Context, []RestoreFile) error
	Paths    []string
	Source   string
	Staged   bool
	Worktree bool
}

// GitShowOpts configures git show queries.
type GitShowOpts struct {
	Ref      string
	Path     string
	MaxBytes int
	Stat     bool
}

// GitShowResult is structured output for git_show.
type GitShowResult struct {
	Ref       string `json:"ref"`
	Path      string `json:"path,omitempty"`
	Kind      string `json:"kind"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Stat      bool   `json:"stat,omitempty"`
}

// GitBlameOpts configures git blame queries.
type GitBlameOpts struct {
	Path      string
	StartLine int
	EndLine   int
	MaxLines  int
}

// GitBlameLine is one blamed source line.
type GitBlameLine struct {
	Line    int       `json:"line"`
	Commit  string    `json:"commit"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	Content string    `json:"content"`
}

// GitRefEntry is one rev-parse result.
type GitRefEntry struct {
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	Peeled string `json:"peeled,omitempty"`
}

// GitBranchEntry is one local branch row.
type GitBranchEntry struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// Commit paths are capped for argv and approval size; restore has a lower destructive scope limit.
const (
	DefaultGitShowMaxBytes      = 256 * 1024
	DefaultGitBlameMaxLines     = 500
	DefaultGitRestoreMaxPaths   = 20
	DefaultGitCommitMaxPaths    = 500
	DefaultGitLogPathMaxCommits = 50
	DefaultGitLogDefaultCommits = 10
	DefaultGitBranchesMax       = 100
	// DefaultGitDiffPageBytes leaves room for JSON escaping below the compaction threshold.
	DefaultGitDiffPageBytes = 5 << 10
	// MaxGitDiffPageBytes permits pages that may be compacted in transit.
	MaxGitDiffPageBytes = DefaultGitShowMaxBytes
)

// GitManager exposes structured repository operations.
type GitManager interface {
	Compare(ctx context.Context, projectDir, base, head string) (GitComparison, error)
	Operate(ctx context.Context, projectDir string, req OperationRequest) (OperationResult, error)
	ListStashes(ctx context.Context, projectDir string, offset, limit int) ([]StashEntry, error)
	CommitDetails(ctx context.Context, projectDir, oid string) (CommitDetails, error)
	BlobSize(ctx context.Context, projectDir, oid string) (int64, error)
	CommitExists(ctx context.Context, projectDir, oid string) (bool, error)
	CommitReview(ctx context.Context, projectDir string, opts CommitReviewOptions) (CommitReviewPage, error)
	CommitStatus(ctx context.Context, projectDir string) (CommitStatus, error)
	Status(ctx context.Context, projectDir string) (*GitStatus, error)
	StatusIgnored(ctx context.Context, projectDir string, paths []string) ([]GitStatusEntry, error)
	ChangedPaths(ctx context.Context, projectDir string) ([]string, error)
	HeadSHA(ctx context.Context, projectDir string) (string, error)
	Diff(ctx context.Context, projectDir string, opts GitDiffOpts) (string, error)
	UntrackedDiff(ctx context.Context, projectDir string) (string, error)
	UntrackedPaths(ctx context.Context, projectDir string, paths []string) ([]string, error)
	UntrackedFileDiff(ctx context.Context, projectDir, rel string) (string, error)
	DiffStat(ctx context.Context, projectDir string, opts GitDiffOpts) ([]GitDiffStatEntry, error)
	Log(ctx context.Context, projectDir string, opts GitLogOpts) ([]GitCommit, error)
	LastTouchByPath(ctx context.Context, projectDir string) (map[string]time.Time, error)
	Show(ctx context.Context, projectDir string, opts GitShowOpts) (*GitShowResult, error)
	TreeOIDs(ctx context.Context, projectDir, ref string, paths []string) (map[string]string, error)
	FileHistory(ctx context.Context, projectDir string, opts GitFileHistoryOpts) ([]GitFileCommit, error)
	IsAncestor(ctx context.Context, projectDir, ancestor, descendant string) (bool, error)
	BlobContent(ctx context.Context, projectDir, blobOID string, maxBytes int) ([]byte, bool, error)
	Blame(ctx context.Context, projectDir string, opts GitBlameOpts) ([]GitBlameLine, error)
	Restore(ctx context.Context, projectDir string, opts GitRestoreOpts) error
	RevParse(ctx context.Context, projectDir string, refs []string) ([]GitRefEntry, error)
	ResolveRevision(ctx context.Context, projectDir, spec string) (RevisionComparison, bool, error)
	Branches(ctx context.Context, projectDir string, maxBranches int) ([]GitBranchEntry, error)
	Init(ctx context.Context, projectDir string) error
	Clone(ctx context.Context, url, destDir string) error
	Commit(ctx context.Context, projectDir string, opts GitCommitOpts) (string, error)
	Stash(ctx context.Context, projectDir string, message string) error
	DiscardAll(ctx context.Context, projectDir string) error
	Push(ctx context.Context, projectDir string) error
	Pull(ctx context.Context, projectDir string) error
	Branch(ctx context.Context, projectDir string) (string, error)
	CreateBranch(ctx context.Context, projectDir string, branch string) error
	Checkout(ctx context.Context, projectDir string, branch string) error
}
