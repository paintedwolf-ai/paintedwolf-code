package native

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type InspectSourceTool struct {
	Boundary *sandbox.Boundary
	Form     FormTool
}

func InspectsGitHead(args map[string]any) bool {
	return args["operation"] == "git_head"
}

func (t *InspectSourceTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if !InspectsGitHead(args) {
		return t.Form.Run(ctx, args, tc)
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	observed, err := t.inspectGitHead(ctx, tc, path)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(observed)
	return string(body), err
}

type gitHead struct {
	Repository bool   `json:"repository"`
	Reference  string `json:"reference,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Unborn     bool   `json:"unborn,omitempty"`
}

func (t *InspectSourceTool) inspectGitHead(ctx context.Context, tc tools.ToolContext, path string) (gitHead, error) {
	target, err := projectpaths.ResolveRead(ctx, t.Boundary, tc, path)
	if err != nil {
		return gitHead{}, err
	}
	dir := target.Abs
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	root := gitrepo.FindRoot(dir)
	if root == "" {
		return gitHead{}, nil
	}
	gitDir, err := t.inspectGitDir(ctx, tc, root)
	if err != nil {
		return gitHead{}, err
	}
	state := gitHead{Repository: true}
	head, err := t.readGitMetadata(ctx, tc, filepath.Join(gitDir, "HEAD"), 4096)
	if err != nil {
		return gitHead{}, err
	}
	if ref, symbolic := strings.CutPrefix(strings.TrimSpace(head), "ref: "); symbolic {
		if !strings.HasPrefix(ref, "refs/") || plumbing.ReferenceName(ref).Validate() != nil {
			return gitHead{}, invalidGitHead(path)
		}
		commonDir, err := t.inspectCommonDir(ctx, tc, gitDir)
		if err != nil {
			return gitHead{}, err
		}
		state.Reference = ref
		state.Commit, err = t.readGitRef(ctx, tc, commonDir, ref)
		state.Unborn = state.Commit == ""
		return state, err
	}
	state.Commit = strings.TrimSpace(head)
	if !validGitHeadCommit(state.Commit) {
		return gitHead{}, invalidGitHead(path)
	}
	return state, nil
}

func (t *InspectSourceTool) inspectGitDir(ctx context.Context, tc tools.ToolContext, root string) (string, error) {
	if _, err := projectpaths.ResolveRead(ctx, t.Boundary, tc, root); err != nil {
		return "", err
	}
	dot := filepath.Join(root, ".git")
	pointer, err := t.readGitMetadata(ctx, tc, dot, 4096)
	if errors.Is(err, projectpaths.ErrIsDirectory) {
		return dot, nil
	}
	if err != nil {
		return "", err
	}
	dir := gitrepo.GitDirFromPointer(root, pointer)
	if dir == "" {
		return "", invalidGitHead(dot)
	}
	return dir, nil
}

func (t *InspectSourceTool) inspectCommonDir(ctx context.Context, tc tools.ToolContext, gitDir string) (string, error) {
	path := filepath.Join(gitDir, "commondir")
	pointer, err := t.readOptionalGitMetadata(ctx, tc, path, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return gitDir, nil
	}
	if err != nil {
		return "", err
	}
	dir := gitrepo.CommonDirFromPointer(gitDir, pointer)
	if dir == "" {
		return "", invalidGitHead(path)
	}
	return dir, nil
}

func (t *InspectSourceTool) readOptionalGitMetadata(ctx context.Context, tc tools.ToolContext, path string, limit int64) (string, error) {
	// Missing entries do not need a read grant. Check the name without following its final link.
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return t.readGitMetadata(ctx, tc, path, limit)
}

func (t *InspectSourceTool) readGitMetadata(ctx context.Context, tc tools.ToolContext, path string, limit int64) (string, error) {
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tc, path)
	if err != nil {
		return "", err
	}
	value, err := resolved.ReadRegularBounded(limit)
	return string(value), err
}

func (t *InspectSourceTool) readGitRef(ctx context.Context, tc tools.ToolContext, commonDir, ref string) (string, error) {
	path := filepath.Join(commonDir, filepath.FromSlash(ref))
	loose, err := t.readOptionalGitMetadata(ctx, tc, path, 4096)
	if err == nil {
		if oid := strings.TrimSpace(loose); validGitHeadCommit(oid) {
			return oid, nil
		}
		return "", invalidGitHead(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	packed, err := t.readOptionalGitMetadata(ctx, tc, filepath.Join(commonDir, "packed-refs"), 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(packed, "\n") {
		oid, name, found := strings.Cut(strings.TrimSpace(line), " ")
		if found && name == ref {
			if validGitHeadCommit(oid) {
				return oid, nil
			}
			return "", invalidGitHead(path)
		}
	}
	return "", nil
}

func validGitHeadCommit(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func invalidGitHead(path string) error {
	return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"tool": "inspect_source", "field": "path", "path": path})
}
