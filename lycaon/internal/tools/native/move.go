package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// MoveTool moves files through the reviewed write capabilities.
type MoveTool struct {
	Boundary *sandbox.Boundary
}

type moveResult struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type moveResponse struct {
	Moved             []moveResult `json:"moved"`
	DestinationCopied []moveResult `json:"destination_copied,omitempty"`
}

// MoveInput is one complete move operation. NoClobber leaves an existing
// destination in place and skips that pair. Directories admits directory
// sources, renamed whole within one root onto an absent destination; the
// rename is journaled like a file rename, so Files history can undo it.
type MoveInput struct {
	Memory      *toolcontract.ProgramMemory `json:"-"`
	Pairs       []FilePair
	NoClobber   bool
	Directories bool
}

// MoveResult lists completed and skipped pairs in order.
type MoveResult struct {
	Moved             []FilePair
	Skipped           []FilePair
	DestinationCopied []FilePair
}

func (t *MoveTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pairs, err := parseMovePairs(args)
	if err != nil {
		return "", err
	}
	result, err := t.Move(ctx, MoveInput{Pairs: pairs}, tctx)
	resp := moveResponse{Moved: make([]moveResult, 0, len(result.Moved))}
	for _, pair := range result.Moved {
		resp.Moved = append(resp.Moved, moveResult(pair))
	}
	for _, pair := range result.DestinationCopied {
		resp.DestinationCopied = append(resp.DestinationCopied, moveResult{From: pair.From, To: pair.To})
	}
	if err != nil {
		if len(resp.Moved)+len(resp.DestinationCopied) > 0 {
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

// Move applies each pair under scope, review and recovery.
func (t *MoveTool) Move(ctx context.Context, in MoveInput, tctx tools.ToolContext) (MoveResult, error) {
	var result MoveResult
	if in.Memory == nil {
		limits, err := toolcontract.CatalogProgramMemoryLimits()
		if err != nil {
			return result, err
		}
		memory, err := toolcontract.NewProgramMemory(limits)
		if err != nil {
			return result, err
		}
		in.Memory = memory
	}
	prof := tctx.ProfileID()
	for _, pair := range in.Pairs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		fromSlash, toSlash := filepath.ToSlash(pair.From), filepath.ToSlash(pair.To)
		srcResolved, err := t.moveSource(ctx, tctx, pair.From, in.Directories)
		if err != nil {
			return result, err
		}
		dstResolved, err := assertMoveWritePath(ctx, t.Boundary, tctx, pair.To, prof)
		if err != nil {
			return result, err
		}
		crossRoot := srcResolved.External != dstResolved.External || srcResolved.Root.ID != dstResolved.Root.ID ||
			filepath.Clean(srcResolved.Root.Path) != filepath.Clean(dstResolved.Root.Path)
		if in.NoClobber {
			if _, err := os.Lstat(dstResolved.Abs); err == nil {
				result.Skipped = append(result.Skipped, FilePair{From: fromSlash, To: toSlash})
				continue
			}
		}
		if crossRoot {
			// The copy operation stages and reviews the destination before publication.
			// Remove the source only after that publication succeeds.
			copyOperation, err := (&CopyTool{Boundary: t.Boundary}).Begin(CopyInput{
				Pairs: []FilePair{pair}, Recursive: in.Directories, NoClobber: in.NoClobber,
			}, in.Memory)
			if err != nil {
				return result, err
			}
			copyResult, err := copyOperation.CopyPair(ctx, pair, tctx)
			copyOperation.Close()
			if err != nil {
				for _, copied := range copyResult.Copied {
					result.DestinationCopied = append(result.DestinationCopied, FilePair{From: copied.From, To: copied.To})
				}
				return result, err
			}
			if len(copyResult.Skipped) > 0 {
				result.Skipped = append(result.Skipped, FilePair{From: fromSlash, To: toSlash})
				continue
			}
			if _, err := (&DeleteTool{Boundary: t.Boundary}).Delete(ctx, DeleteInput{
				Memory: in.Memory, Paths: []string{pair.From}, Recursive: in.Directories,
			}, tctx); err != nil {
				result.DestinationCopied = append(result.DestinationCopied, FilePair{From: fromSlash, To: toSlash})
				return result, fmt.Errorf("move %s -> %s after copy: %w", pair.From, pair.To, err)
			}
			result.Moved = append(result.Moved, FilePair{From: fromSlash, To: toSlash})
			continue
		}
		var displacedBytes int64
		var reviewDisplacedPolicy func() ([]sourceeffect.ReviewedFile, error)
		if srcInfo, srcErr := os.Lstat(srcResolved.Abs); srcErr == nil && srcInfo.IsDir() {
			if dstInfo, dstErr := os.Lstat(dstResolved.Abs); dstErr == nil && dstInfo.IsDir() {
				summary, inspectErr := inspectDeleteTree(ctx, t.Boundary, tctx, dstResolved, prof, in.Memory, nil, false)
				if inspectErr != nil {
					return result, fmt.Errorf("move %s -> %s: %w", pair.From, pair.To, inspectErr)
				}
				displacedBytes = summary.bytes
				if len(summary.policyTargets) > 0 {
					policyTargets := summary.policyTargets
					reviewDisplacedPolicy = func() ([]sourceeffect.ReviewedFile, error) {
						reviewed := make([]sourceeffect.ReviewedFile, 0, len(policyTargets))
						for _, target := range policyTargets {
							item, err := reviewAgentTreePolicyRemoval(ctx, tctx, target, in.Memory)
							if err != nil {
								return nil, err
							}
							reviewed = append(reviewed, item)
						}
						return reviewed, nil
					}
				}
			}
		}
		if err := renameAgentEntry(ctx, tctx, resolvedMutationTarget(srcResolved), resolvedMutationTarget(dstResolved), displacedBytes, in.Memory, reviewDisplacedPolicy); err != nil {
			return result, fmt.Errorf("move %s -> %s: %w", pair.From, pair.To, err)
		}
		result.Moved = append(result.Moved, FilePair{From: fromSlash, To: toSlash})
		afterSuccessfulMutation(ctx, tctx, fromSlash, toSlash)
	}
	return result, nil
}

// moveSource resolves a file source, or with directories a directory entry
// itself; a link to a directory stays a file-style entry.
func (t *MoveTool) moveSource(ctx context.Context, tctx tools.ToolContext, from string, directories bool) (projectpaths.Resolved, error) {
	prof := tctx.ProfileID()
	entry, err := assertMoveWritePath(ctx, t.Boundary, tctx, from, prof)
	if err != nil {
		return projectpaths.Resolved{}, err
	}
	info, statErr := os.Lstat(entry.Abs)
	if statErr == nil && info.IsDir() && directories {
		return entry, nil
	}
	resolved, err := assertMutationFileSource(ctx, t.Boundary, tctx, from, prof, "move", "MOVE_NOT_FOUND", "MOVE_IS_DIRECTORY", "MOVE_SYMLINK_ESCAPE", assertMoveWritePath)
	if err != nil {
		return projectpaths.Resolved{}, err
	}
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return entry, nil
	}
	return resolved, nil
}
