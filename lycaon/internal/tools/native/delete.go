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
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// DeleteTool removes files and empty directories within write scope.
type DeleteTool struct {
	Boundary *sandbox.Boundary
	// Caps overrides the catalog recursive mutation ceilings; zero uses them.
	Caps RecursiveCaps
}

func (t *DeleteTool) Name() string { return "delete" }

type deleteResponse struct {
	Deleted []string `json:"deleted"`
}

// DeleteInput is one complete delete operation. Recursive removes each tree
// children first, every entry through the same reviewed, recoverable removal,
// after measuring the whole tree against the catalog's recursive mutation
// caps. Force skips operands that do not exist.
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
		entries := []string{resolved.Abs}
		if info.IsDir() {
			if in.FilesOnly {
				return result, &toolrejection.ToolReject{Code: "DELETE_IS_DIRECTORY", Data: map[string]any{"path": relSlash}}
			}
			if entries, err = deletionOrder(t.Caps, resolved.Abs, relSlash, in.Recursive); err != nil {
				return result, err
			}
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			target, err := assertDeleteWritePath(ctx, t.Boundary, tctx, entry, prof)
			if err != nil {
				return result, err
			}
			rel := filepath.ToSlash(target.ScopeRel)
			if entry == resolved.Abs {
				rel = relSlash
			}
			if err := removeAgentPath(ctx, tctx, resolvedMutationTarget(target)); err != nil {
				return result, fmt.Errorf("delete %s: %w", rel, err)
			}
			result.Deleted = append(result.Deleted, rel)
			afterSuccessfulMutation(ctx, tctx, rel)
		}
	}
	return result, nil
}

// deletionOrder lists a directory's entries children first. Without recursion
// only an empty directory is removable. Links are entries, never descended.
func deletionOrder(caps RecursiveCaps, dir, relSlash string, recursive bool) ([]string, error) {
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("delete %s: %w", relSlash, err)
	}
	if len(children) > 0 && !recursive {
		return nil, &toolrejection.ToolReject{Code: "DELETE_NOT_EMPTY", Data: map[string]any{"path": relSlash, "entries": len(children)}}
	}
	var order []string
	err = walkRecursiveTree(caps, dir, relSlash, func(path string, isDir bool) error {
		order = append(order, path)
		return nil
	})
	return order, err
}

// walkRecursiveTree visits a tree children first after measuring it against
// the catalog's recursive mutation caps, so no visit runs for a tree that
// recovery could not hold. Links are entries and are never followed.
func walkRecursiveTree(caps RecursiveCaps, dir, relSlash string, visit func(path string, isDir bool) error) error {
	caps, err := caps.resolve()
	if err != nil {
		return err
	}
	maxFiles, maxBytes := caps.Files, caps.Bytes
	type entry struct {
		path  string
		isDir bool
	}
	var order []entry
	var files, bytes int64
	var walk func(string) error
	walk = func(path string) error {
		children, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, child := range children {
			full := filepath.Join(path, child.Name())
			info, err := os.Lstat(full)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			files++
			if info.Mode().IsRegular() {
				bytes += info.Size()
			}
			if files > maxFiles {
				return recursiveCapRefusal("recursive_mutation_files", "files", maxFiles, files, relSlash)
			}
			if bytes > maxBytes {
				return recursiveCapRefusal("recursive_mutation_bytes", "bytes", maxBytes, bytes, relSlash)
			}
			order = append(order, entry{full, false})
		}
		order = append(order, entry{path, true})
		return nil
	}
	if err := walk(dir); err != nil {
		return err
	}
	for _, e := range order {
		if err := visit(e.path, e.isDir); err != nil {
			return err
		}
	}
	return nil
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
