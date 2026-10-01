package git

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
)

// CommitDetails describes one immutable commit, independent of HEAD.
type CommitDetails struct {
	Hash        string
	Parents     []string
	Message     string
	AuthorName  string
	AuthoredAt  time.Time
	CommittedAt time.Time
}

// CommitReviewFile addresses both sides without following working-tree paths.
type CommitReviewFile struct {
	Path       string
	BeforePath string
	Status     string
	BeforeOID  string
	AfterOID   string
	BeforeMode string
	AfterMode  string
	Insertions int
	Deletions  int
	Binary     bool
}

type CommitReviewPage struct {
	Files      []CommitReviewFile
	Total      int
	Insertions int
	Deletions  int
	NextOffset int
}

type CommitReviewOptions struct {
	Before string
	After  string
	Offset int
	Limit  int
	// Path selects one row from the complete comparison, preserving renames.
	Path string
}

// FullObjectID reports whether value is a complete SHA-1 or SHA-256 object id.
func FullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// CommitExists reports whether the repository holds a commit named by a
// hexadecimal object id or unambiguous prefix of one.
func (m *Manager) CommitExists(ctx context.Context, projectDir, oid string) (bool, error) {
	if len(oid) < 4 || len(oid) > 64 || strings.Trim(oid, "0123456789abcdef") != "" {
		return false, nil
	}
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return false, err
	}
	_, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "cat-file", "-e", oid + "^{commit}"}, hermeticOpts(0))
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// BlobSize permits refusing oversized content before reading the object.
func (m *Manager) BlobSize(ctx context.Context, projectDir, oid string) (int64, error) {
	if !FullObjectID(oid) {
		return 0, errors.New("a full object id is required")
	}
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return 0, err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "cat-file", "-s", oid}, hermeticOpts(0))
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, fmt.Errorf("object size exited %d", code)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || size < 0 {
		return 0, errors.New("invalid Git object size")
	}
	return size, nil
}

func (m *Manager) CommitDetails(ctx context.Context, projectDir, oid string) (CommitDetails, error) {
	if !FullObjectID(oid) {
		return CommitDetails{}, errors.New("a full commit object id is required")
	}
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return CommitDetails{}, err
	}
	key := dir + "\x00" + oid
	commit, _, err := m.commits.load(ctx, key, func(ctx context.Context) (CommitDetails, int, error) {
		value, err := readCommitDetails(ctx, dir, oid)
		size := len(key) + len(value.Message) + len(value.AuthorName) + len(value.Parents)*64 + 256
		return value, size, err
	})
	commit.Parents = append([]string(nil), commit.Parents...)
	return commit, err
}

func readCommitDetails(ctx context.Context, dir, oid string) (CommitDetails, error) {
	fields := make([]string, 0, 5)
	args := []string{"--no-replace-objects", "show", "-s", "--no-notes", "--no-show-signature", "--format=format:%H%x00%an%x00%aI%x00%cI%x00%B%x00", oid, "--"}
	err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), func(raw []byte) error {
		if len(fields) == 5 {
			return errors.New("invalid commit metadata record count")
		}
		fields = append(fields, string(raw))
		return nil
	})
	if err != nil {
		return CommitDetails{}, err
	}
	if len(fields) != 5 || fields[0] != oid {
		return CommitDetails{}, errors.New("invalid commit metadata")
	}
	authored, err := time.Parse(time.RFC3339, fields[2])
	if err != nil {
		return CommitDetails{}, err
	}
	committed, err := time.Parse(time.RFC3339, fields[3])
	if err != nil {
		return CommitDetails{}, err
	}
	parents, err := commitObjectParents(ctx, dir, oid)
	if err != nil {
		return CommitDetails{}, err
	}
	return CommitDetails{Hash: fields[0], Parents: parents, AuthorName: fields[1], AuthoredAt: authored, CommittedAt: committed, Message: strings.TrimRight(fields[4], "\n")}, nil
}

// Formatted history omits parents at shallow boundaries; the object does not.
func commitObjectParents(ctx context.Context, dir, oid string) ([]string, error) {
	opts := hermeticOpts(0)
	opts.MaxOutput = gitexec.MaxRecordBytes + 1
	raw, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "cat-file", "commit", gitargv.EndOfOptions, oid}, opts)
	if err != nil {
		return nil, err
	}
	if code != 0 || len(raw) > gitexec.MaxRecordBytes {
		return nil, errors.New("commit header could not be read")
	}
	header, _, found := strings.Cut(string(raw), "\n\n")
	if !found {
		return nil, errors.New("invalid commit header")
	}
	parents := []string{}
	for _, line := range strings.Split(header, "\n") {
		if parent, ok := strings.CutPrefix(line, "parent "); ok {
			if !FullObjectID(parent) {
				return nil, errors.New("invalid commit parent")
			}
			parents = append(parents, parent)
		}
	}
	return parents, nil
}

// CommitReview serves pages from one immutable, bounded comparison snapshot.
func (m *Manager) CommitReview(ctx context.Context, projectDir string, opts CommitReviewOptions) (CommitReviewPage, error) {
	if !FullObjectID(opts.After) || (opts.Before != "" && !FullObjectID(opts.Before)) {
		return CommitReviewPage{}, errors.New("full commit object ids are required")
	}
	if opts.Offset < 0 || opts.Limit < 1 || opts.Limit > 500 {
		return CommitReviewPage{}, errors.New("invalid commit review page")
	}
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return CommitReviewPage{}, err
	}
	key := dir + "\x00" + opts.Before + "\x00" + opts.After
	snapshot, _, err := m.reviews.load(ctx, key, func(ctx context.Context) (commitReviewSnapshot, int, error) {
		complete := CommitReviewOptions{Before: opts.Before, After: opts.After, Limit: int(^uint(0) >> 1)}
		page, err := readCommitReview(ctx, dir, complete, true)
		if errors.Is(err, errReviewCacheLimit) {
			return commitReviewSnapshot{oversized: true}, len(key) + 64, nil
		}
		size := len(key) + 64
		for _, file := range page.Files {
			size += reviewFileBytes(file)
		}
		return commitReviewSnapshot{page: page}, size, err
	})
	if err != nil {
		return CommitReviewPage{}, err
	}
	if snapshot.oversized {
		return readCommitReview(ctx, dir, opts, false)
	}
	return projectCommitReview(snapshot.page, opts), nil
}

func readCommitReview(ctx context.Context, dir string, opts CommitReviewOptions, bounded bool) (CommitReviewPage, error) {
	args := []string{"--no-replace-objects", "--attr-source=" + opts.After, "diff-tree", "--no-commit-id", "-r", "-z", "--no-abbrev", "--relative", "--find-renames", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none"}
	if opts.Before == "" {
		args = append(args, "--root", opts.After)
	} else {
		args = append(args, opts.Before, opts.After)
	}
	parser := newCommitReviewParser(opts)
	parser.bounded = bounded
	rawArgs := append(append([]string{}, args...), "--raw", "--", ".")
	if err := gitexec.RunRecords(ctx, dir, rawArgs, hermeticOpts(0), parser.raw); err != nil {
		return CommitReviewPage{}, err
	}
	if parser.pending != nil {
		return CommitReviewPage{}, errors.New("incomplete commit path record")
	}
	if opts.Path != "" {
		return parser.page, nil
	}
	statArgs := append(append([]string{}, args...), "--numstat", "--", ".")
	if err := gitexec.RunRecords(ctx, dir, statArgs, hermeticOpts(0), parser.stat); err != nil {
		return CommitReviewPage{}, err
	}
	if parser.statPaths != 0 {
		return CommitReviewPage{}, errors.New("incomplete commit stats record")
	}
	if parser.statCount != parser.page.Total {
		return CommitReviewPage{}, errors.New("commit paths and stats disagree")
	}
	if opts.Offset+len(parser.page.Files) < parser.page.Total {
		parser.page.NextOffset = opts.Offset + len(parser.page.Files)
	}
	return parser.page, nil
}
