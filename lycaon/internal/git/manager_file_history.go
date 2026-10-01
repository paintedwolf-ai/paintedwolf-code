package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// DefaultFileHistoryPage bounds one page of a file's commit lineage.
const DefaultFileHistoryPage = 30

// MaxFileHistoryDepth caps positional paging.
const MaxFileHistoryDepth = 600

const fileHistoryBudget = 30 * time.Second

// ErrFileHistoryTimeout reports an exhausted page budget.
var ErrFileHistoryTimeout = errors.New("git file history timed out")

type GitFileHistoryOpts struct {
	// Path is the file's current path, relative to the invocation directory.
	Path string
	// Revision pins paging to a full commit ID. Empty selects the current HEAD.
	Revision string
	// Skip and Limit page from the tip.
	Skip  int
	Limit int
}

type GitFileCommit struct {
	Hash       string
	AuthorName string
	Subject    string
	// Path addresses the file within this commit.
	Path string
	// BlobOID is empty when the commit removed the file.
	BlobOID     string
	AuthoredAt  time.Time
	CommittedAt time.Time
}

// FileHistory returns independent copies of current-path commits, newest first.
func (m *Manager) FileHistory(ctx context.Context, projectDir string, opts GitFileHistoryOpts) (commits []GitFileCommit, err error) {
	started := time.Now()
	disposition := "bypass"
	defer func() {
		slog.InfoContext(ctx, "git file history", "cache", disposition,
			"duration_ms", time.Since(started).Milliseconds(), "commits", len(commits),
			"path", opts.Path, "revision", opts.Revision, "skip", opts.Skip, "limit", opts.Limit,
			"outcome", fileHistoryOutcome(err))
	}()
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	dir = gitrepo.CanonicalDir(dir)
	if opts.Path == "" || strings.ContainsRune(opts.Path, 0) {
		return nil, fmt.Errorf("file history requires a path without NUL bytes")
	}
	opts = boundedFileHistory(opts)
	if opts.Skip >= MaxFileHistoryDepth {
		return nil, nil
	}
	if opts.Revision == "" {
		opts.Revision, err = m.HeadSHA(ctx, dir)
		if err != nil {
			return nil, err
		}
	}
	if !validObjectID(opts.Revision) {
		return nil, fmt.Errorf("file history requires a full commit ID")
	}
	key, cacheable := fileHistoryCacheKey(dir, opts)
	read := func(work context.Context) ([]GitFileCommit, int, error) {
		rows, readErr := readFileHistory(work, dir, opts)
		// Ancestry overrides can appear during the walk.
		after, safe := fileHistoryCacheKey(dir, opts)
		if !safe || after != key {
			return rows, 0, readErr
		}
		return rows, fileHistoryBytes(key, rows), readErr
	}
	if !cacheable {
		commits, err = readFileHistory(ctx, dir, opts)
	} else {
		commits, disposition, err = m.histories.load(ctx, key, read)
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		err = ErrFileHistoryTimeout
	}
	return slices.Clone(commits), err
}

func boundedFileHistory(opts GitFileHistoryOpts) GitFileHistoryOpts {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = DefaultFileHistoryPage
	}
	if opts.Skip < 0 {
		opts.Skip = 0
	}
	if opts.Skip < MaxFileHistoryDepth && opts.Skip+opts.Limit > MaxFileHistoryDepth {
		opts.Limit = MaxFileHistoryDepth - opts.Skip
	}
	return opts
}

func readFileHistory(ctx context.Context, dir string, opts GitFileHistoryOpts) (rows []GitFileCommit, err error) {
	started := time.Now()
	defer func() {
		slog.InfoContext(ctx, "git file history acquisition", "duration_ms", time.Since(started).Milliseconds(),
			"outcome", fileHistoryOutcome(err), "commits", len(rows),
			"path", opts.Path, "revision", opts.Revision, "skip", opts.Skip, "limit", opts.Limit)
	}()
	args := []string{
		"--no-replace-objects", "--literal-pathspecs", "log", "--raw", "-z", "--no-abbrev", "--no-renames",
		"--no-show-signature", "--no-notes", "--no-follow", "--no-use-mailmap", "--no-color", "--encoding=UTF-8", "--root", "--no-relative", "--diff-merges=off",
		"--format=%x00%H%x00%an%x00%at%x00%ct%x00%s%x00",
		fmt.Sprintf("--max-count=%d", opts.Limit), fmt.Sprintf("--skip=%d", opts.Skip),
		opts.Revision, "--", opts.Path,
	}
	walkCtx, cancel := context.WithTimeout(ctx, fileHistoryBudget)
	defer cancel()
	out, code, err := gitexec.Run(walkCtx, dir, args, hermeticOpts(fileHistoryBudget))
	if failure := fileHistoryFailure(ctx, walkCtx, err, code); failure != nil {
		return nil, failure
	}
	if code != 0 {
		return nil, fmt.Errorf("git log failed: %s", strings.TrimSpace(string(out)))
	}
	return parseFileHistory(string(out))
}

func fileHistoryOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrFileHistoryTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}

func fileHistoryFailure(parent, walk context.Context, err error, code int) error {
	if err == nil {
		return nil
	}
	switch {
	case parent.Err() != nil:
		return parent.Err()
	case errors.Is(err, exec.ErrTimeout),
		errors.Is(walk.Err(), context.DeadlineExceeded):
		return ErrFileHistoryTimeout
	case code <= 0:
		return err
	}
	return nil
}

// IsAncestor reports whether descendant reaches ancestor.
func (m *Manager) IsAncestor(
	ctx context.Context,
	projectDir, ancestor, descendant string,
) (bool, error) {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return false, err
	}
	ancestor, descendant = strings.TrimSpace(ancestor), strings.TrimSpace(descendant)
	if ancestor == "" || descendant == "" {
		return false, nil
	}
	out, code, err := gitexec.Run(ctx, dir,
		[]string{"merge-base", "--is-ancestor", gitargv.EndOfOptions, ancestor, descendant}, hermeticOpts(0))
	if err != nil {
		return false, err
	}
	// Exit 1 is a valid negative result; other nonzero statuses are errors.
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: exit %d: %s",
			ancestor, descendant, code, strings.TrimSpace(string(out)))
	}
}

// BlobContent reads one bounded blob from the repository store.
func (m *Manager) BlobContent(
	ctx context.Context,
	projectDir, blobOID string,
	maxBytes int,
) ([]byte, bool, error) {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return nil, false, err
	}
	blobOID = strings.TrimSpace(blobOID)
	if blobOID == "" {
		return nil, false, nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultGitShowMaxBytes
	}
	opts := hermeticOpts(0)
	opts.MaxOutput = int64(maxBytes) + 1
	out, code, err := gitexec.Run(ctx, dir,
		[]string{"--no-replace-objects", "cat-file", "blob", gitargv.EndOfOptions, blobOID}, opts)
	if errors.Is(err, exec.ErrOutputTruncated) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if code != 0 {
		return nil, false, nil
	}
	if len(out) > maxBytes {
		return nil, false, nil
	}
	return out, true, nil
}
