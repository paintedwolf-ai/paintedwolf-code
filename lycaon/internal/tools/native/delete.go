package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// DeleteTool removes files and empty directories within write scope.
type DeleteTool struct {
	Boundary *sandbox.Boundary
}

func (t *DeleteTool) Name() string { return "delete" }

type deleteResponse struct {
	Deleted []string `json:"deleted"`
}

// DeleteInput is one complete delete operation. Recursive removes each tree
// as one reviewed, recoverable operation. A tree exceeding the history
// owner's recovery byte quota requires explicit permanent-removal review. Force skips operands that do not exist.
type DeleteInput struct {
	Paths     []string
	FilesOnly bool
	Recursive bool
	Force     bool
}

// DeleteResult lists removed entries in removal order, including the entries
// already removed when a later one fails.
type DeleteResult struct {
	Deleted []string
}

func (t *DeleteTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paths, err := parseDeletePaths(args)
	if err != nil {
		return "", err
	}
	filesOnly, _ := args["files_only"].(bool)
	result, err := t.Delete(ctx, DeleteInput{Paths: paths, FilesOnly: filesOnly}, tctx)
	resp := deleteResponse{Deleted: result.Deleted}
	if resp.Deleted == nil {
		resp.Deleted = []string{}
	}
	if err != nil {
		if len(resp.Deleted) > 0 {
			return interruptedMutationBatch(resp, err)
		}
		return "", err
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Delete performs the complete removal under scope, review and recovery.
func (t *DeleteTool) Delete(ctx context.Context, in DeleteInput, tctx tools.ToolContext) (DeleteResult, error) {
	var result DeleteResult
	prof := tctx.ProfileID()
	for _, path := range in.Paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		relSlash := filepath.ToSlash(path)
		resolved, err := assertDeleteWritePath(ctx, t.Boundary, tctx, path, prof)
		if err != nil {
			return result, err
		}
		info, err := os.Lstat(resolved.Abs)
		if err != nil {
			if os.IsNotExist(err) {
				if in.Force {
					continue
				}
				return result, &toolrejection.ToolReject{Code: "DELETE_NOT_FOUND", Data: map[string]any{"path": relSlash}}
			}
			return result, fmt.Errorf("delete %s: %w", path, err)
		}
		if info.IsDir() {
			if in.FilesOnly {
				return result, &toolrejection.ToolReject{Code: "DELETE_IS_DIRECTORY", Data: map[string]any{"path": relSlash}}
			}
			children, readErr := os.ReadDir(resolved.Abs)
			if readErr != nil {
				return result, fmt.Errorf("delete %s: %w", relSlash, readErr)
			}
			if len(children) > 0 && !in.Recursive {
				return result, &toolrejection.ToolReject{Code: "DELETE_NOT_EMPTY", Data: map[string]any{"path": relSlash, "entries": len(children)}}
			}
			if len(children) > 0 {
				summary, inspectErr := inspectDeleteTree(ctx, t.Boundary, tctx, resolved, prof)
				if inspectErr != nil {
					return result, inspectErr
				}
				displayPath := relSlash
				if resolved.DisplayPath != "" {
					displayPath = filepath.ToSlash(resolved.DisplayPath)
				} else if resolved.ScopeRel != "" {
					displayPath = filepath.ToSlash(resolved.ScopeRel)
				}
				if err := removeAgentTree(ctx, tctx, resolvedMutationTarget(resolved), displayPath, summary.bytes, func() error {
					_, err := inspectDeleteTree(ctx, t.Boundary, tctx, resolved, prof)
					return err
				}); err != nil {
					return result, fmt.Errorf("delete %s: %w", relSlash, err)
				}
				result.Deleted = append(result.Deleted, relSlash)
				afterSuccessfulMutation(ctx, tctx, relSlash)
				continue
			}
		}
		if err := removeAgentPath(ctx, tctx, resolvedMutationTarget(resolved)); err != nil {
			return result, fmt.Errorf("delete %s: %w", relSlash, err)
		}
		result.Deleted = append(result.Deleted, relSlash)
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	return result, nil
}

type deleteTreeSummary struct {
	files int64
	bytes int64
}

func inspectDeleteTree(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, root projectpaths.Resolved, prof string) (deleteTreeSummary, error) {
	var summary deleteTreeSummary
	checkPins := len(tctx.Turn.TurnWritePinGlobs) > 0
	var walk func(absDir, relDir string) error
	walk = func(absDir, relDir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := os.ReadDir(absDir)
		if err != nil {
			return err
		}
		for _, child := range children {
			full := filepath.Join(absDir, child.Name())
			childRel := filepath.ToSlash(filepath.Join(relDir, child.Name()))
			info, err := child.Info()
			if err != nil {
				info, err = os.Lstat(full)
				if err != nil {
					return err
				}
			}
			_, policy := tctx.AgentPolicyTarget(full)
			_, deniedGit := tools.IsGitInternalsWritePath(childRel)
			if checkPins || policy || deniedGit || tools.IsSensitivePath(childRel) {
				if _, err := assertDeleteWritePath(ctx, b, tctx, full, prof); err != nil {
					return err
				}
			}
			if info.IsDir() {
				if err := walk(full, childRel); err != nil {
					return err
				}
				continue
			}
			summary.files++
			if info.Mode().IsRegular() {
				summary.bytes += info.Size()
			}
		}
		return nil
	}
	err := walk(root.Abs, filepath.ToSlash(root.ScopeRel))
	return summary, err
}

// RecursiveCaps bound one recursive mutation's files and regular-file bytes so
// every entry stays within source-ledger recovery.
type RecursiveCaps struct {
	Files, Bytes int64
}

// CatalogRecursiveCaps are the frozen catalog's recursive_mutation_files and
// recursive_mutation_bytes ceilings.
func CatalogRecursiveCaps() (RecursiveCaps, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return RecursiveCaps{}, err
	}
	var caps RecursiveCaps
	for _, limit := range catalog.Language.Limits {
		switch limit.ID {
		case "recursive_mutation_files":
			caps.Files = limit.Maximum
		case "recursive_mutation_bytes":
			caps.Bytes = limit.Maximum
		}
	}
	if caps.Files < 1 || caps.Bytes < 1 {
		return RecursiveCaps{}, fmt.Errorf("compiled language is missing recursive mutation ceilings")
	}
	return caps, nil
}

func (c RecursiveCaps) resolve() (RecursiveCaps, error) {
	if c.Files > 0 && c.Bytes > 0 {
		return c, nil
	}
	return CatalogRecursiveCaps()
}

func recursiveCapRefusal(limit, unit string, maximum, observed int64, path string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{
		"path": path, "limit_id": limit, "resource_unit": unit, "maximum": maximum, "observed": observed}}
}
