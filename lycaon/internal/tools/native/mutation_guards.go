package native

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

const (
	hostChmodMaxPaths         = 20
	hostDeleteMaxPaths        = 20
	hostCopyMaxPairs          = 10
	hostMoveMaxPairs          = 10
	hostMkdirMaxPaths         = 20
	hostChownMaxPaths         = 20
	defaultCopyMaxBytes int64 = 10 << 20 // 10 MiB
)

type filePair struct {
	From string
	To   string
}

// guardMutationContent rejects proposed text the text gateway cannot retain:
// content above the mutation budget, or bytes that cannot reopen as text.
func guardMutationContent(tool, path, content string) error {
	_, err := textfile.EncodeBounded(content, textfile.UTF8, textfile.LimitsForRaw(readcaps.MaxMutationBytes))
	if err == nil {
		return nil
	}
	if errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge) {
		return sourceview.MutationSizeReject(tool, path, int64(len(content)))
	}
	return &tools.ToolReject{
		Code: "WRITE_BINARY_DENIED",
		Data: map[string]any{
			"path":           path,
			"bytes":          len(content),
			"reason":         err.Error(),
			"max_file_bytes": readcaps.MaxMutationBytes,
			"tool":           tool,
		},
	}
}

func parseChmodPaths(args map[string]any) ([]string, error) {
	return parseBoundedPaths(args, hostChmodMaxPaths, func(max, got int) error {
		return &tools.ToolReject{
			Code: "CHMOD_BULK_DENIED",
			Data: map[string]any{"max_paths": max, "requested": got},
		}
	})
}

func parseChownPaths(args map[string]any) ([]string, error) {
	return parseBoundedPaths(args, hostChownMaxPaths, func(max, got int) error {
		return &tools.ToolReject{
			Code: "CHOWN_BULK_DENIED",
			Data: map[string]any{"max_paths": max, "requested": got},
		}
	})
}

func parseDeletePaths(args map[string]any) ([]string, error) {
	return parseBoundedPaths(args, hostDeleteMaxPaths, func(max, got int) error {
		return &tools.ToolReject{
			Code: "DELETE_BULK_DENIED",
			Data: map[string]any{"max_paths": max, "requested": got},
		}
	})
}

func parseCopyPairs(args map[string]any) ([]filePair, int64, error) {
	maxBytes := defaultCopyMaxBytes
	if raw, present := args["max_file_bytes"]; present {
		maxBytes = 0
		switch value := raw.(type) {
		case float64:
			if value > 0 && value < float64(math.MaxInt64) && math.Trunc(value) == value {
				maxBytes = int64(value)
			}
		case int:
			maxBytes = int64(value)
		case int64:
			maxBytes = value
		}
		if maxBytes <= 0 {
			return nil, 0, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "max_file_bytes must be a positive signed 64-bit integer"})
		}
	}

	pairs, err := parseFilePairs(args, "copies", hostCopyMaxPairs, "COPY_BULK_DENIED")
	return pairs, maxBytes, err
}

func parseMovePairs(args map[string]any) ([]filePair, error) {
	return parseFilePairs(args, "moves", hostMoveMaxPairs, "MOVE_BULK_DENIED")
}

func parseMkdirPaths(args map[string]any) ([]string, error) {
	return parseBoundedPaths(args, hostMkdirMaxPaths, func(max, got int) error {
		return &tools.ToolReject{
			Code: "MKDIR_BULK_DENIED",
			Data: map[string]any{"max_paths": max, "requested": got},
		}
	})
}

func parseFilePairs(args map[string]any, key string, maxItems int, bulkCode string) ([]filePair, error) {
	raw, ok := args[key].([]any)
	if !ok || len(raw) == 0 {
		return nil, toolkit.MissingArg(key)
	}
	if len(raw) > maxItems {
		return nil, &tools.ToolReject{
			Code: bulkCode,
			Data: map[string]any{"max_pairs": maxItems, "requested": len(raw)},
		}
	}
	out := make([]filePair, 0, len(raw))
	for _, item := range raw {
		pairMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid %q entry", key)
		}
		from, _ := pairMap["from"].(string)
		to, _ := pairMap["to"].(string)
		if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("invalid %q entry: need from and to", key)
		}
		out = append(out, filePair{
			From: strings.TrimSpace(from),
			To:   strings.TrimSpace(to),
		})
	}
	return out, nil
}

func parseBoundedPaths(args map[string]any, maxItems int, overLimit func(max, got int) error) ([]string, error) {
	raw, ok := args["paths"].([]any)
	if !ok || len(raw) == 0 {
		return nil, toolkit.MissingArg("paths")
	}
	if len(raw) > maxItems {
		return nil, overLimit(maxItems, len(raw))
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("invalid \"paths\" entry")
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out, nil
}

func assertChmodTarget(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (target mutationTarget, modeBefore os.FileMode, err error) {
	resolved, err := assertChmodWritePath(ctx, boundary, tctx, relPath, profileID)
	if err != nil {
		return mutationTarget{}, 0, err
	}
	info, err := os.Lstat(resolved.Abs)
	if err != nil {
		if os.IsNotExist(err) {
			return mutationTarget{}, 0, &tools.ToolReject{
				Code: "CHMOD_NOT_FOUND",
				Data: map[string]any{"path": filepath.ToSlash(relPath)},
			}
		}
		return mutationTarget{}, 0, fmt.Errorf("chmod %s: %w", relPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(resolved.Abs)
		if readErr != nil {
			return mutationTarget{}, 0, fmt.Errorf("chmod %s: read symlink: %w", relPath, readErr)
		}
		resolvedRel := sourceview.SymlinkTarget(resolved.Root.Path, resolved.ScopeRel, target)
		if resolvedRel == "" {
			return mutationTarget{}, 0, &tools.ToolReject{
				Code: "CHMOD_SYMLINK_ESCAPE",
				Data: map[string]any{"path": filepath.ToSlash(relPath)},
			}
		}
		if err := boundary.AssertWriteScope(ctx, resolved.Root.Path, resolvedRel, profileID); err != nil {
			if reject := writeScopeReject(ctx, boundary, resolvedRel, profileID, "chmod", err); !errors.Is(reject, err) {
				return mutationTarget{}, 0, reject
			}
			return mutationTarget{}, 0, &tools.ToolReject{
				Code: "CHMOD_SYMLINK_ESCAPE",
				Data: map[string]any{"path": filepath.ToSlash(relPath), "symlink_target": resolvedRel},
			}
		}
		// The descriptor walk refuses symlinks, so the mutation targets the
		// in-scope link destination the guard just approved.
		fullPath, resolveErr := boundary.ResolveAbs(resolved.Root.Path, resolvedRel)
		if resolveErr != nil {
			return mutationTarget{}, 0, resolveErr
		}
		info, err = os.Lstat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return mutationTarget{}, 0, &tools.ToolReject{
					Code: "CHMOD_NOT_FOUND",
					Data: map[string]any{"path": filepath.ToSlash(relPath)},
				}
			}
			return mutationTarget{}, 0, fmt.Errorf("chmod %s: %w", relPath, err)
		}
		resolved.Abs, resolved.ScopeRel = fullPath, resolvedRel
	}
	return resolvedMutationTarget(resolved), info.Mode(), nil
}

func assertChownTarget(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (target mutationTarget, uidBefore, gidBefore int, err error) {
	resolved, err := assertChownWritePath(ctx, boundary, tctx, relPath, profileID)
	if err != nil {
		return mutationTarget{}, 0, 0, err
	}
	info, err := os.Lstat(resolved.Abs)
	if err != nil {
		if os.IsNotExist(err) {
			return mutationTarget{}, 0, 0, &tools.ToolReject{
				Code: "CHOWN_NOT_FOUND",
				Data: map[string]any{"path": filepath.ToSlash(relPath)},
			}
		}
		return mutationTarget{}, 0, 0, fmt.Errorf("chown %s: %w", relPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(resolved.Abs)
		if readErr != nil {
			return mutationTarget{}, 0, 0, fmt.Errorf("chown %s: read symlink: %w", relPath, readErr)
		}
		resolvedRel := sourceview.SymlinkTarget(resolved.Root.Path, resolved.ScopeRel, target)
		if resolvedRel == "" {
			return mutationTarget{}, 0, 0, &tools.ToolReject{
				Code: "CHOWN_SYMLINK_ESCAPE",
				Data: map[string]any{"path": filepath.ToSlash(relPath)},
			}
		}
		if err := boundary.AssertWriteScope(ctx, resolved.Root.Path, resolvedRel, profileID); err != nil {
			if reject := writeScopeReject(ctx, boundary, resolvedRel, profileID, "chown", err); !errors.Is(reject, err) {
				return mutationTarget{}, 0, 0, reject
			}
			return mutationTarget{}, 0, 0, &tools.ToolReject{
				Code: "CHOWN_SYMLINK_ESCAPE",
				Data: map[string]any{"path": filepath.ToSlash(relPath), "symlink_target": resolvedRel},
			}
		}
		fullPath, resolveErr := boundary.ResolveAbs(resolved.Root.Path, resolvedRel)
		if resolveErr != nil {
			return mutationTarget{}, 0, 0, resolveErr
		}
		resolved.Abs, resolved.ScopeRel = fullPath, resolvedRel
	}
	uidBefore, gidBefore, err = fileOwnership(resolved.Abs)
	if err != nil {
		return mutationTarget{}, 0, 0, fmt.Errorf("chown %s: %w", relPath, err)
	}
	return resolvedMutationTarget(resolved), uidBefore, gidBefore, nil
}

func assertChownWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "chown", func(path string) error {
		return &tools.ToolReject{Code: "CHOWN_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}

func assertChmodWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "chmod", func(path string) error {
		return &tools.ToolReject{Code: "CHMOD_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}

func assertDeleteWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "delete", func(path string) error {
		return &tools.ToolReject{Code: "DELETE_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}

func assertCopyWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "copy", func(path string) error {
		return &tools.ToolReject{Code: "COPY_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}

func assertMoveWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "move", func(path string) error {
		return &tools.ToolReject{Code: "MOVE_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}

func assertMkdirWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "mkdir", func(path string) error {
		return &tools.ToolReject{Code: "MKDIR_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}

func assertMutationFileSource(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID, tool string,
	notFoundCode, isDirCode, symlinkEscapeCode string,
	assertPath func(context.Context, *sandbox.Boundary, tools.ToolContext, string, string) (projectpaths.Resolved, error),
) (projectpaths.Resolved, error) {
	resolved, err := assertPath(ctx, boundary, tctx, relPath, profileID)
	if err != nil {
		return projectpaths.Resolved{}, err
	}
	info, err := os.Lstat(resolved.Abs)
	if err != nil {
		if os.IsNotExist(err) {
			return projectpaths.Resolved{}, &tools.ToolReject{
				Code: notFoundCode,
				Data: map[string]any{"path": filepath.ToSlash(relPath)},
			}
		}
		return projectpaths.Resolved{}, fmt.Errorf("%s %s: %w", tool, relPath, err)
	}
	if info.IsDir() {
		return projectpaths.Resolved{}, &tools.ToolReject{
			Code: isDirCode,
			Data: map[string]any{"path": filepath.ToSlash(relPath)},
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(resolved.Abs)
		if readErr != nil {
			return projectpaths.Resolved{}, fmt.Errorf("%s %s: read symlink: %w", tool, relPath, readErr)
		}
		resolvedRel := sourceview.SymlinkTarget(resolved.Root.Path, resolved.ScopeRel, target)
		if resolvedRel == "" {
			return projectpaths.Resolved{}, &tools.ToolReject{
				Code: symlinkEscapeCode,
				Data: map[string]any{"path": filepath.ToSlash(relPath)},
			}
		}
		if err := boundary.AssertWriteScope(ctx, resolved.Root.Path, resolvedRel, profileID); err != nil {
			if reject := writeScopeReject(ctx, boundary, resolvedRel, profileID, tool, err); !errors.Is(reject, err) {
				return projectpaths.Resolved{}, reject
			}
			return projectpaths.Resolved{}, &tools.ToolReject{
				Code: symlinkEscapeCode,
				Data: map[string]any{"path": filepath.ToSlash(relPath), "symlink_target": resolvedRel},
			}
		}
		fullPath, err := boundary.ResolveAbs(resolved.Root.Path, resolvedRel)
		if err != nil {
			return projectpaths.Resolved{}, err
		}
		resolved.Abs = fullPath
		resolved.ScopeRel = resolvedRel
		return resolved, nil
	}
	return resolved, nil
}

func assertWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID, tool string,
	deny func(string) error,
) (projectpaths.Resolved, error) {
	if sandbox.HasParentTraversal(relPath) {
		return projectpaths.Resolved{}, deny(filepath.ToSlash(relPath))
	}
	relSlash := filepath.ToSlash(relPath)
	// Credential and git-internals paths are refused by the mutation floor in
	// projectpaths.ResolveWrite, which both calls below cross; this guard adds
	// only profile scope.
	if err := assertProfileWriteScope(ctx, boundary, tctx, relPath, tool); err != nil {
		return projectpaths.Resolved{}, writePathReject(ctx, boundary, relSlash, profileID, tool, err, deny)
	}
	resolved, err := projectpaths.ResolveWrite(ctx, boundary, tctx, relPath)
	if err != nil {
		return projectpaths.Resolved{}, writePathReject(ctx, boundary, relSlash, profileID, tool, err, deny)
	}
	return resolved, nil
}

func writePathReject(
	ctx context.Context,
	boundary *sandbox.Boundary,
	relSlash, profileID, tool string,
	err error,
	deny func(string) error,
) error {
	if reject := writeScopeReject(ctx, boundary, relSlash, profileID, tool, err); !errors.Is(reject, err) {
		return reject
	}
	var structured *tools.ToolReject
	if errors.As(err, &structured) || tools.HostRefusal(err) != nil {
		return err
	}
	return deny(relSlash)
}
