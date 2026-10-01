package toolhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/paginate"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitPageFiles bounds each Git inventory request independently of transcript compaction.
const GitPageFiles = 80

// gitStatusPage reads the paging, selection, and rollup args for git_status.
func gitStatusPage(args map[string]any) git.StatusToolPage {
	return git.StatusToolPage{
		Paths:      stringSliceArg(args, "paths"),
		Offset:     toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000),
		Limit:      toolkit.ClampIntArg(args, "limit", GitPageFiles, 1, GitPageFiles),
		GroupDepth: toolkit.ClampIntArg(args, "group_depth", 0, 0, 16),
		Summary:    args["summary"] == true,
		Ignored:    args["ignored"] == true,
	}
}

// gitDiffQuery is one git_diff call: what to compare, which page, how much text.
type gitDiffQuery struct {
	opts      git.GitDiffOpts
	stat      bool
	untracked bool
	page      git.DiffToolPage
}

func gitDiffQueryFromArgs(args map[string]any) gitDiffQuery {
	staged, _ := args["staged"].(bool)
	stat, _ := args["stat"].(bool)
	untracked, _ := args["untracked"].(bool)
	baseRef, _ := args["base_ref"].(string)
	headRef, _ := args["head_ref"].(string)
	return gitDiffQuery{
		opts:      git.GitDiffOpts{Paths: stringSliceArg(args, "paths"), Staged: staged, BaseRef: strings.TrimSpace(baseRef), HeadRef: strings.TrimSpace(headRef)},
		stat:      stat,
		untracked: untracked,
		page: git.DiffToolPage{
			Offset:   toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000),
			Limit:    toolkit.ClampIntArg(args, "limit", GitPageFiles, 1, GitPageFiles),
			MaxBytes: toolkit.ClampIntArg(args, "max_bytes", git.DefaultGitDiffPageBytes, 1, git.MaxGitDiffPageBytes),
		},
	}
}

// gitDiffHandler returns the complete selected page for screened output projection.
func gitDiffHandler(m git.GitManager) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		q := gitDiffQueryFromArgs(args)
		cwd := tctx.ActiveRootPath()
		resp, err := gitDiffPage(ctx, m, cwd, q)
		if err == nil {
			for _, file := range resp.Files {
				tctx.RecordSourcePath(file.Path, api.NavigationEntryKindFile)
			}
		}
		return git.MarshalDiffToolResponse(resp, err)
	}
}

func gitDiffPage(ctx context.Context, m git.GitManager, cwd string, q gitDiffQuery) (git.DiffToolResponse, error) {
	resp := git.DiffToolResponse{
		Stat:      q.stat,
		Staged:    q.opts.Staged,
		BaseRef:   q.opts.BaseRef,
		HeadRef:   q.opts.HeadRef,
		Paths:     q.opts.Paths,
		Untracked: q.untracked,
		Offset:    q.page.Offset,
		Limit:     q.page.Limit,
	}
	if !q.stat {
		resp.MaxBytes = q.page.MaxBytes
	}
	if q.opts.HeadRef != "" {
		refs, err := m.RevParse(ctx, cwd, []string{q.opts.BaseRef + "^{commit}", q.opts.HeadRef + "^{commit}"})
		if err != nil {
			return resp, err
		}
		if len(refs) != 2 {
			return resp, fmt.Errorf("comparison did not resolve both Git tips")
		}
		resp.BaseOID, resp.HeadOID = refs[0].SHA, refs[1].SHA
		q.opts.BaseRef, q.opts.HeadRef = resp.BaseOID, resp.HeadOID
	}
	all, err := m.DiffStat(ctx, cwd, q.opts)
	if err != nil {
		return resp, err
	}
	if q.untracked {
		paths, err := m.UntrackedPaths(ctx, cwd, q.opts.Paths)
		if err != nil {
			return resp, err
		}
		for _, p := range paths {
			all = append(all, git.GitDiffStatEntry{Path: p, Untracked: true})
		}
	}
	page, total, truncated, next := paginate.Slice(all, q.page.Offset, q.page.Limit)
	resp.FilesTotal, resp.FilesTruncated, resp.NextOffset = total, truncated, next
	entries := make([]git.DiffToolEntry, 0, len(page))
	for _, f := range page {
		entries = append(entries, git.DiffToolEntry{Path: f.Path, Insertions: f.Insertions, Deletions: f.Deletions, Untracked: f.Untracked})
	}
	if err := attachHunks(ctx, m, cwd, q, entries); err != nil {
		return resp, err
	}
	for i := range entries {
		if q.stat {
			entries[i].Diff = ""
		} else {
			entries[i].DiffBytes = len(entries[i].Diff)
		}
	}
	resp.Files = entries
	return resp, nil
}

// attachHunks splits tracked diffs and synthesizes untracked additions, including their line counts.
func attachHunks(ctx context.Context, m git.GitManager, cwd string, q gitDiffQuery, entries []git.DiffToolEntry) error {
	var tracked []string
	var trackedIdx []int
	for i, e := range entries {
		if e.Untracked {
			hunk, err := m.UntrackedFileDiff(ctx, cwd, e.Path)
			if err != nil {
				return err
			}
			entries[i].Diff = hunk
			entries[i].Insertions, entries[i].Deletions = git.CountHunkLines(hunk)
			continue
		}
		tracked = append(tracked, e.Path)
		trackedIdx = append(trackedIdx, i)
	}
	if q.stat || len(tracked) == 0 {
		return nil
	}
	opts := q.opts
	opts.Paths = tracked
	text, err := m.Diff(ctx, cwd, opts)
	if err != nil {
		return err
	}
	blocks := git.SplitDiffBlocks(text)
	if len(blocks) != len(trackedIdx) {
		return fmt.Errorf("git diff listed %d files where numstat listed %d", len(blocks), len(trackedIdx))
	}
	for n, i := range trackedIdx {
		entries[i].Diff = blocks[n]
	}
	return nil
}

func gitLogHandler(m git.GitManager) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		ref, _ := args["ref"].(string)
		all, _ := args["all"].(bool)
		if ref == "" && !all {
			ref = "HEAD"
		}
		defaultLimit := git.DefaultGitLogDefaultCommits
		if path != "" {
			defaultLimit = git.DefaultGitLogPathMaxCommits
		}
		opts := git.GitLogOpts{
			Limit:  toolkit.ClampIntArg(args, "limit", defaultLimit, 1, git.MaxGitLogPage),
			Offset: toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000),
			Ref:    ref,
			Path:   path,
			All:    all,
		}
		query := opts
		query.Limit++
		commits, err := m.Log(ctx, tctx.ActiveRootPath(), query)
		return git.MarshalHistoryPage(commits, opts, err)
	}
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if s, ok := p.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func gitStatusOutput(status *git.GitStatus, page git.StatusToolPage, err error, tctx tools.ToolContext) (string, error) {
	if err != nil {
		return git.MarshalToolFailure(err)
	}
	response := git.BuildStatusToolResponse(status, page)
	for _, file := range response.Files {
		tctx.RecordSourcePath(file.Path, api.NavigationEntryKindFile)
	}
	return git.MarshalStatusToolResponse(response, nil)
}

func gitStatusIgnoredOutput(ctx context.Context, m git.GitManager, cwd string, page git.StatusToolPage, tctx tools.ToolContext) (string, error) {
	if len(page.Paths) == 0 {
		return git.MarshalToolFailure(fmt.Errorf("git status with ignored requires explicit paths"))
	}
	entries, err := m.StatusIgnored(ctx, cwd, page.Paths)
	if err != nil {
		return git.MarshalToolFailure(err)
	}
	pageEntries, total, truncated, next := paginate.Slice(entries, page.Offset, page.Limit)
	if pageEntries == nil {
		pageEntries = []git.GitStatusEntry{}
	}
	resp := git.StatusToolResponse{
		Available:      true,
		Paths:          page.Paths,
		Offset:         page.Offset,
		Limit:          page.Limit,
		Ignored:        true,
		Files:          pageEntries,
		FilesTotal:     total,
		FilesTruncated: truncated,
		NextOffset:     next,
	}
	for _, file := range resp.Files {
		tctx.RecordSourcePath(file.Path, api.NavigationEntryKindFile)
	}
	return git.MarshalStatusToolResponse(resp, nil)
}
