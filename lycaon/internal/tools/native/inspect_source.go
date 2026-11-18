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
	repo, ok := gitrepo.Discover(dir)
	if !ok {
		return gitHead{}, nil
	}
	if repo.GitDir == "" || repo.CommonDir == "" {
		return gitHead{}, invalidGitHead(path)
	}
	if _, err := projectpaths.ResolveRead(ctx, t.Boundary, tc, repo.Root); err != nil {
		return gitHead{}, err
	}
	state := gitHead{Repository: true}
	head, err := t.readGitMetadata(ctx, tc, filepath.Join(repo.GitDir, "HEAD"), 4096)
	if err != nil {
		return gitHead{}, err
	}
	if ref, symbolic := strings.CutPrefix(strings.TrimSpace(head), "ref: "); symbolic {
		if !strings.HasPrefix(ref, "refs/") || plumbing.ReferenceName(ref).Validate() != nil {
			return gitHead{}, invalidGitHead(path)
		}
		state.Reference = ref
		state.Commit, err = t.readGitRef(ctx, tc, repo.CommonDir, ref)
		state.Unborn = state.Commit == ""
		return state, err
	}
	state.Commit = strings.TrimSpace(head)
	if !validGitHeadCommit(state.Commit) {
		return gitHead{}, invalidGitHead(path)
	}
	return state, nil
}

func (t *InspectSourceTool) readGitMetadata(ctx context.Context, tc tools.ToolContext, path string, limit int64) (string, error) {
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tc, path)
	if err != nil {
		return "", err
	}
	value, err := resolved.ReadBounded(limit)
	return string(value), err
}

func (t *InspectSourceTool) readGitRef(ctx context.Context, tc tools.ToolContext, commonDir, ref string) (string, error) {
	path := filepath.Join(commonDir, filepath.FromSlash(ref))
	loose, err := t.readGitMetadata(ctx, tc, path, 4096)
	if err == nil {
		if oid := strings.TrimSpace(loose); validGitHeadCommit(oid) {
			return oid, nil
		}
		return "", invalidGitHead(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	packed, err := t.readGitMetadata(ctx, tc, filepath.Join(commonDir, "packed-refs"), 4<<20)
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
