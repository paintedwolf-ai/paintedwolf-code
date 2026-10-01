package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/repochange"
)

// Show returns commit patch or file content at ref.
func (m *Manager) Show(ctx context.Context, projectDir string, opts GitShowOpts) (*GitShowResult, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(opts.Ref)
	if ref == "" {
		ref = "HEAD"
	}
	if err := validateGitRef(ref, "ref"); err != nil {
		return nil, err
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultGitShowMaxBytes
	}
	path := opts.Path

	var args []string
	kind := "commit"
	if path != "" {
		kind = "blob"
		if opts.Stat {
			args = []string{"show", "--no-color", "--stat", gitargv.EndOfOptions, ref, "--", filepath.ToSlash(path)}
		} else {
			args = []string{"show", "--no-color", gitargv.EndOfOptions, ref + ":" + filepath.ToSlash(path)}
		}
	} else {
		if opts.Stat {
			args = []string{"show", "--no-color", "--stat", gitargv.EndOfOptions, ref}
		} else {
			args = []string{"show", "--no-color", "--format=fuller", gitargv.EndOfOptions, ref}
		}
	}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git show failed: %s", strings.TrimSpace(string(out)))
	}
	content, truncated := truncateUTF8(out, maxBytes)
	return &GitShowResult{
		Ref:       ref,
		Path:      filepath.ToSlash(path),
		Kind:      kind,
		Content:   content,
		Truncated: truncated,
		Stat:      opts.Stat,
	}, nil
}

// Blame returns per-line attribution for a path.
func (m *Manager) Blame(ctx context.Context, projectDir string, opts GitBlameOpts) ([]GitBlameLine, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	path := opts.Path
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if binary, binErr := isBinaryWorktreeFile(dir, path); binErr == nil && binary {
		return nil, fmt.Errorf("binary file blame not supported")
	}
	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = DefaultGitBlameMaxLines
	}
	start := opts.StartLine
	end := opts.EndLine
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end < start {
		end = start + maxLines - 1
	}
	if end-start+1 > maxLines {
		end = start + maxLines - 1
	}
	args := []string{"blame", "--porcelain",
		"-L", fmt.Sprintf("%d,%d", start, end),
		"--", path,
	}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		msg := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("git blame failed: %s", msg)
	}
	return parseBlamePorcelain(out)
}

// Restore applies Git's native restore modes to explicit paths.
func (m *Manager) Restore(ctx context.Context, projectDir string, opts GitRestoreOpts) error {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return err
	}
	if len(opts.Paths) == 0 {
		return fmt.Errorf("paths are required")
	}
	source := strings.TrimSpace(opts.Source)
	staged, worktree := opts.Staged, opts.Worktree
	if !staged && !worktree {
		worktree = true
	}
	if source == "" && staged {
		source = "HEAD"
	}
	if source != "" {
		if err := validateGitRef(source, "restore source"); err != nil {
			return err
		}
	}
	if opts.Review != nil {
		prepared, err := prepareRestoreReview(ctx, dir, source, staged, worktree, opts.Paths)
		if err != nil {
			return err
		}
		defer prepared.close()
		if err := opts.Review(ctx, prepared.files); err != nil {
			return err
		}
		release, err := gitlease.Repository(ctx, dir)
		if err != nil {
			return err
		}
		defer release()
		if err := prepared.validate(ctx, dir); err != nil {
			return err
		}
		source = prepared.source
	} else {
		release, err := gitlease.Repository(ctx, dir)
		if err != nil {
			return err
		}
		defer release()
	}
	args := []string{"--literal-pathspecs", "restore"}
	if source != "" {
		args = append(args, "--source="+source)
	}
	if staged {
		args = append(args, "--staged")
	}
	if worktree {
		args = append(args, "--worktree")
	}
	args = append(args, "--")
	args = append(args, opts.Paths...)
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git restore failed: %s", strings.TrimSpace(string(out)))
	}
	notifyRepoChange(ctx, dir, repochange.WorktreeChanged)
	return nil
}

// RevParse resolves refs to SHAs (peeled for annotated tags).
func (m *Manager) RevParse(ctx context.Context, projectDir string, refs []string) ([]GitRefEntry, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("refs are required")
	}
	out := make([]GitRefEntry, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if err := validateGitRef(ref, "ref"); err != nil {
			return nil, err
		}
		shaOut, code, runErr := gitexec.Run(ctx, dir, []string{"rev-parse", "--verify", gitargv.EndOfOptions, ref}, hermeticOpts(0))
		if runErr != nil {
			return nil, runErr
		}
		if code != 0 {
			return nil, fmt.Errorf("git rev-parse %q failed: %s", ref, strings.TrimSpace(string(shaOut)))
		}
		entry := GitRefEntry{
			Ref: ref,
			SHA: strings.TrimSpace(string(shaOut)),
		}
		peeledOut, peelCode, peelErr := gitexec.Run(ctx, dir, []string{"rev-parse", "--verify", gitargv.EndOfOptions, ref + "^{}"}, hermeticOpts(0))
		if peelErr == nil && peelCode == 0 {
			peeled := strings.TrimSpace(string(peeledOut))
			if peeled != "" && peeled != entry.SHA {
				entry.Peeled = peeled
			}
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("refs are required")
	}
	return out, nil
}

// Branches lists local branches and marks the current branch.
func (m *Manager) Branches(ctx context.Context, projectDir string, maxBranches int) ([]GitBranchEntry, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	if maxBranches <= 0 {
		maxBranches = DefaultGitBranchesMax
	}
	current, curErr := m.Branch(ctx, projectDir)
	if curErr != nil {
		current = ""
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"for-each-ref",
		"--format=%(refname:short)",
		"refs/heads/",
	}, hermeticOpts(0))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git for-each-ref failed: %s", strings.TrimSpace(string(out)))
	}
	var branches []GitBranchEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		branches = append(branches, GitBranchEntry{
			Name:    name,
			Current: name == current,
		})
		if len(branches) >= maxBranches {
			break
		}
	}
	return branches, nil
}

func parseBlamePorcelain(raw []byte) ([]GitBlameLine, error) {
	lines := strings.Split(string(raw), "\n")
	var out []GitBlameLine
	var cur *GitBlameLine
	for _, line := range lines {
		if strings.HasPrefix(line, "\t") {
			if cur == nil {
				continue
			}
			cur.Content = strings.TrimPrefix(line, "\t")
			out = append(out, *cur)
			cur = nil
			continue
		}
		if line == "" || line == "boundary" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && len(fields[0]) >= 7 {
			finalLine, err := strconv.Atoi(fields[2])
			if err != nil {
				continue
			}
			cur = &GitBlameLine{
				Line:   finalLine,
				Commit: strings.TrimPrefix(fields[0], "^"),
			}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "author ") {
			cur.Author = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "author-time ") {
			sec, parseErr := strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64)
			if parseErr == nil {
				cur.Date = time.Unix(sec, 0).UTC()
			}
		}
	}
	return out, nil
}

func truncateUTF8(b []byte, max int) (string, bool) {
	if len(b) <= max {
		return string(b), false
	}
	return string(b[:max]), true
}

func isBinaryWorktreeFile(projectDir, relPath string) (bool, error) {
	full := filepath.Join(projectDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		return false, err
	}
	return bytes.IndexByte(data, 0) >= 0, nil
}
