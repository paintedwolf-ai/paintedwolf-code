package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// GitShowTool shows commit or file content at a ref.
type GitShowTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

func (t *GitShowTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	ref, _ := args["ref"].(string)
	path, _ := args["path"].(string)
	stat, _ := args["stat"].(bool)
	maxBytes := intArg(args, "max_bytes", git.DefaultGitShowMaxBytes)
	cwd, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, tools.HostWriteRoot(tctx), tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return git.MarshalShowToolResponse(nil, prepErr)
	}
	defer func() { _ = finish() }()
	if path = strings.TrimSpace(path); path != "" {
		if err := assertGitReadPath(ctx, t.Boundary, tctx, path, tctx.ProfileID(), "git_show"); err != nil {
			return "", err
		}
	}
	show, err := t.Git.Show(ctx, cwd, git.GitShowOpts{
		Ref:      ref,
		Path:     path,
		MaxBytes: maxBytes,
		Stat:     stat,
	})
	if err == nil && path != "" {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	if err == nil && show != nil && !show.Stat {
		if show.Kind == "blob" {
			abs, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, show.Path)
			if resolveErr != nil {
				return "", resolveErr
			}
			if deliveryErr := tctx.ObserveCredentialDelivery(ctx, abs, show.Path, show.Content); deliveryErr != nil {
				return "", deliveryErr
			}
		} else if deliveryErr := deliverGitPatchCredentials(ctx, tctx, show.Content); deliveryErr != nil {
			return "", deliveryErr
		}
	}
	return git.MarshalShowToolResponse(show, err)
}

// GitBlameTool attributes lines in a file.
type GitBlameTool struct {
	Git      git.GitManager
	Boundary *sandbox.Boundary
}

func (t *GitBlameTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	path, _ := args["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if err := assertGitReadPath(ctx, t.Boundary, tctx, path, tctx.ProfileID(), "git_blame"); err != nil {
		return "", err
	}
	cwd, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, tools.HostWriteRoot(tctx), tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return git.MarshalBlameToolResponse(nil, prepErr)
	}
	defer func() { _ = finish() }()
	lines, err := t.Git.Blame(ctx, cwd, git.GitBlameOpts{
		Path:      path,
		StartLine: intArg(args, "start_line", 0),
		EndLine:   intArg(args, "end_line", 0),
		MaxLines:  intArg(args, "max_lines", git.DefaultGitBlameMaxLines),
	})
	if err == nil {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	if err == nil {
		var content strings.Builder
		for _, line := range lines {
			content.WriteString(line.Content)
			content.WriteByte('\n')
		}
		abs, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, path)
		if resolveErr != nil {
			return "", resolveErr
		}
		if deliveryErr := tctx.ObserveCredentialDelivery(ctx, abs, path, content.String()); deliveryErr != nil {
			return "", deliveryErr
		}
	}
	return git.MarshalBlameToolResponse(lines, err)
}

// GitRefTool resolves refs to SHAs.
type GitRefTool struct {
	Git git.GitManager
}

func (t *GitRefTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	refs := stringSliceFromArg(args, "refs")
	if len(refs) == 0 {
		return "", toolkit.MissingArg("refs")
	}
	cwd, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, tools.HostWriteRoot(tctx), tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return git.MarshalRefToolResponse(nil, prepErr)
	}
	defer func() { _ = finish() }()
	out, err := t.Git.RevParse(ctx, cwd, refs)
	return git.MarshalRefToolResponse(out, err)
}

// GitBranchesTool lists local branches.
type GitBranchesTool struct {
	Git git.GitManager
}

func (t *GitBranchesTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	max := intArg(args, "max_branches", git.DefaultGitBranchesMax)
	cwd, finish, _, prepErr := privategit.PrepareViewGitOperation(ctx, tools.HostWriteRoot(tctx), tctx.Identity.WorkerJobID)
	if prepErr != nil {
		return git.MarshalBranchesToolResponse(nil, prepErr)
	}
	defer func() { _ = finish() }()
	branches, err := t.Git.Branches(ctx, cwd, max)
	return git.MarshalBranchesToolResponse(branches, err)
}

func assertGitReadPath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID, tool string,
) error {
	if sandbox.HasParentTraversal(relPath) {
		return fmt.Errorf("path contains path escape")
	}
	relSlash := filepath.ToSlash(relPath)
	if tools.IsSensitivePath(relSlash) {
		return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": relSlash, "tool": tool}}
	}
	if boundary == nil {
		return nil
	}
	if _, err := projectpaths.ResolveRead(ctx, boundary, tctx, relPath); err != nil {
		// Preserve the resolver's structured rejection.
		var structured *toolrejection.ToolReject
		if errors.As(err, &structured) || toolrejection.HostRefusal(err) != nil {
			return err
		}
		return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": relSlash, "tool": tool}}
	}
	return nil
}

func stringSliceFromArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func intArg(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return defaultVal
}
