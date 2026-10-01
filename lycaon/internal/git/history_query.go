package git

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

const MaxGitLogPage = 80

// resolveCommit freezes a single revision and rejects ambiguous or non-commit objects.
func resolveCommit(ctx context.Context, dir, ref string) (string, error) {
	if err := validateGitRef(ref, "ref"); err != nil {
		return "", err
	}
	out, err := restoreGit(ctx, dir, hermeticOpts(0), "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validObjectID(oid) {
		return "", fmt.Errorf("invalid commit object identity")
	}
	return oid, nil
}

// validObjectID accepts lowercase hex only: it reads ids git itself printed.
func validObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// historyRevision accepts one revision or Git's two- and three-dot ranges.
func historyRevision(ctx context.Context, dir, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	separator := ".."
	if strings.Contains(ref, "...") {
		separator = "..."
	}
	parts := strings.Split(ref, separator)
	if len(parts) > 2 {
		return "", fmt.Errorf("expected a revision or one revision range")
	}
	for i, part := range parts {
		if part == "" {
			part = "HEAD"
		}
		oid, err := resolveCommit(ctx, dir, part)
		if err != nil {
			return "", err
		}
		parts[i] = oid
	}
	return strings.Join(parts, separator), nil
}

func (m *Manager) Log(ctx context.Context, projectDir string, opts GitLogOpts) ([]GitCommit, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultGitLogDefaultCommits
		if opts.Path != "" {
			limit = DefaultGitLogPathMaxCommits
		}
	}
	if limit > MaxGitLogPage+1 || opts.Offset < 0 {
		return nil, fmt.Errorf("history page exceeds bounds")
	}
	var ref string
	if opts.Ref != "" || !opts.All {
		var err error
		ref, err = historyRevision(ctx, dir, opts.Ref)
		if err != nil {
			return nil, err
		}
	}
	args := []string{"log", "-z", "--format=%H%x00%an%x00%at%x00%s", fmt.Sprintf("--max-count=%d", limit), fmt.Sprintf("--skip=%d", opts.Offset)}
	if opts.All {
		args = append(args, "--all")
	}
	args = append(args, "--end-of-options")
	if ref != "" {
		args = append(args, ref)
	}
	args = append(args, "--")
	if opts.Path != "" {
		args = append(args, ":(literal)"+opts.Path)
	}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git log failed: %s", out)
	}
	return parseHistory(out)
}

func parseHistory(out []byte) ([]GitCommit, error) {
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	commits := []GitCommit{}
	if len(out) == 0 {
		return commits, nil
	}
	if len(fields)%4 != 0 {
		return nil, fmt.Errorf("malformed Git history records")
	}
	for i := 0; i < len(fields); i += 4 {
		sec, err := strconv.ParseInt(fields[i+2], 10, 64)
		if err != nil || !validObjectID(fields[i]) {
			return nil, fmt.Errorf("malformed Git history metadata")
		}
		commits = append(commits, GitCommit{Hash: fields[i], Author: fields[i+1], Date: time.Unix(sec, 0).UTC(), Subject: fields[i+3]})
	}
	return commits, nil
}

// GitComparison counts commits unique to each frozen tip.
type GitComparison struct {
	BaseRef    string   `json:"base_ref"`
	HeadRef    string   `json:"head_ref"`
	BaseOID    string   `json:"base_oid"`
	HeadOID    string   `json:"head_oid"`
	Ahead      int      `json:"ahead"`
	Behind     int      `json:"behind"`
	MergeBases []string `json:"merge_bases"`
}

func (m *Manager) Compare(ctx context.Context, projectDir, base, head string) (GitComparison, error) {
	result := GitComparison{BaseRef: base, HeadRef: head, MergeBases: []string{}}
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return result, err
	}
	result.BaseOID, err = resolveCommit(ctx, dir, base)
	if err != nil {
		return result, err
	}
	result.HeadOID, err = resolveCommit(ctx, dir, head)
	if err != nil {
		return result, err
	}
	out, err := restoreGit(ctx, dir, hermeticOpts(0), "rev-list", "--left-right", "--count", result.BaseOID+"..."+result.HeadOID)
	if err != nil {
		return result, err
	}
	if _, err = fmt.Sscanf(string(out), "%d %d", &result.Behind, &result.Ahead); err != nil {
		return result, err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"merge-base", "--all", "--", result.BaseOID, result.HeadOID}, hermeticOpts(0))
	if err != nil {
		return result, err
	}
	if code > 1 {
		return result, fmt.Errorf("merge-base failed: %s", out)
	}
	if code == 0 {
		result.MergeBases = strings.Fields(string(out))
	}
	return result, nil
}

// MarshalHistoryPage preserves an empty list and advertises further pages.
func MarshalHistoryPage(commits []GitCommit, opts GitLogOpts, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	next := (*int)(nil)
	if len(commits) > opts.Limit {
		commits = commits[:opts.Limit]
		n := opts.Offset + len(commits)
		next = &n
	}
	if commits == nil {
		commits = []GitCommit{}
	}
	raw, err := surveyjson.Marshal(struct {
		Available  bool        `json:"available"`
		Ref        string      `json:"ref,omitempty"`
		All        bool        `json:"all,omitempty"`
		Commits    []GitCommit `json:"commits"`
		Offset     int         `json:"offset"`
		NextOffset *int        `json:"next_offset,omitempty"`
	}{true, opts.Ref, opts.All, commits, opts.Offset, next})
	return string(raw), err
}
