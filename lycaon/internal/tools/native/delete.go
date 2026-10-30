package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceeffect"
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
	Memory    *toolcontract.ProgramMemory `json:"-"`
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
	resp := deleteResponse(result)
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
	if in.Memory == nil {
		limits, err := toolcontract.CatalogProgramMemoryLimits()
		if err != nil {
			return result, err
		}
		in.Memory, err = toolcontract.NewProgramMemory(limits)
		if err != nil {
			return result, err
		}
	}
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
			if !in.Recursive {
				children, readErr := countMutationChildren(ctx, resolved.EffectLocation(), in.Memory)
				if readErr != nil {
					return result, fmt.Errorf("delete %s: %w", relSlash, readErr)
				}
				if children > 0 {
					return result, &toolrejection.ToolReject{Code: "DELETE_NOT_EMPTY", Data: map[string]any{"path": relSlash, "entries": children}}
				}
			}
			if in.Recursive {
				summary, inspectErr := inspectDeleteTree(ctx, t.Boundary, tctx, resolved, prof, in.Memory, nil, true)
				if inspectErr != nil {
					return result, inspectErr
				}
				displayPath := relSlash
				if resolved.DisplayPath != "" {
					displayPath = filepath.ToSlash(resolved.DisplayPath)
				} else if resolved.ScopeRel != "" {
					displayPath = filepath.ToSlash(resolved.ScopeRel)
				}
				if err := removeAgentTree(ctx, tctx, resolvedMutationTarget(resolved), displayPath, summary.bytes, in.Memory, func() ([]sourceeffect.ReviewedFile, error) {
					if len(summary.policyTargets) == 0 {
						return nil, nil
					}
					reviewed := make([]sourceeffect.ReviewedFile, 0, len(summary.policyTargets))
					for _, target := range summary.policyTargets {
						item, err := reviewAgentTreePolicyRemoval(ctx, tctx, target, in.Memory)
						if err != nil {
							return nil, err
						}
						reviewed = append(reviewed, item)
					}
					return reviewed, nil
				}); err != nil {
					return result, fmt.Errorf("delete %s: %w", relSlash, err)
				}
				result.Deleted = append(result.Deleted, summary.deleted...)
				result.Deleted = append(result.Deleted, relSlash)
				afterSuccessfulMutation(ctx, tctx, relSlash)
				continue
			}
		}
		if err := removeAgentPath(ctx, tctx, resolvedMutationTarget(resolved), in.Memory); err != nil {
			return result, fmt.Errorf("delete %s: %w", relSlash, err)
		}
		result.Deleted = append(result.Deleted, relSlash)
		afterSuccessfulMutation(ctx, tctx, relSlash)
	}
	return result, nil
}

type deleteTreeSummary struct {
	files         int64
	bytes         int64
	policyTargets []projectpaths.Resolved
	deleted       []string
}

func inspectDeleteTree(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, selected projectpaths.Resolved, prof string, memory *toolcontract.ProgramMemory, reviewPolicy func(projectpaths.Resolved) error, recordDeleted bool) (deleteTreeSummary, error) {
	var summary deleteTreeSummary
	checkPins := len(tctx.Turn.TurnWritePinGlobs) > 0
	classifyPolicy := confine.AgentPolicyClassifier(tools.ConfineRootsForAction(tctx)...)
	root, err := fseffect.OpenReadRoot(selected.EffectLocation().Root)
	if err != nil {
		return summary, err
	}
	defer func() { _ = root.Close() }()
	visit := func(rel string, info os.FileInfo) error {
		if rel == "." {
			return nil
		}
		full := filepath.Join(selected.Abs, rel)
		childRel := filepath.ToSlash(filepath.Join(selected.ScopeRel, rel))
		_, policy := classifyPolicy(full)
		_, deniedGit := tools.IsGitInternalsWritePath(childRel)
		if checkPins || policy || deniedGit || tools.IsSensitivePath(childRel) {
			target, err := assertDeleteWritePath(ctx, b, tctx, full, prof)
			if err != nil {
				return err
			}
			if policy {
				summary.policyTargets = append(summary.policyTargets, target)
				if reviewPolicy != nil {
					if err := reviewPolicy(target); err != nil {
						return err
					}
				}
			}
		}
		if !info.IsDir() {
			summary.files++
			if info.Mode().IsRegular() {
				summary.bytes += info.Size()
			}
			if recordDeleted {
				summary.deleted = append(summary.deleted, childRel)
			}
		}
		return nil
	}
	var after func(string, os.FileInfo) error
	if recordDeleted {
		after = func(rel string, _ os.FileInfo) error {
			if rel != "." {
				summary.deleted = append(summary.deleted, filepath.ToSlash(filepath.Join(selected.ScopeRel, rel)))
			}
			return nil
		}
	}
	err = walkMutationTree(ctx, root, selected.EffectLocation().Rel, ".", visit, after, memory)
	return summary, err
}

func countMutationChildren(ctx context.Context, location fseffect.Location, memory *toolcontract.ProgramMemory) (int, error) {
	release, err := memory.ReserveProgramState(ctx, 2*128*int64(2048+len(location.Root)+len(location.Rel)))
	if err != nil {
		return 0, err
	}
	defer release()
	dir, err := fseffect.OpenRead(location)
	if err != nil {
		return 0, err
	}
	defer func() { _ = dir.Close() }()
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entries, err := dir.ReadDir(128)
		count += len(entries)
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// RecursiveCaps bound one recursive mutation's files and regular-file bytes so
// every entry stays within source-ledger recovery.
type RecursiveCaps struct {
	Files, Bytes int64
}

// CatalogRecursiveCaps are the frozen catalog's recursive_mutation_files and
// recursive_mutation_bytes ceilings.
func CatalogRecursiveCaps() (RecursiveCaps, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
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
