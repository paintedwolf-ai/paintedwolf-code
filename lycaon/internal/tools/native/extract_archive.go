package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ExtractArchiveTool unpacks .zip or .tar.gz archives into a write-scoped directory.
type ExtractArchiveTool struct {
	Boundary *sandbox.Boundary
}

func (t *ExtractArchiveTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	archivePath, destPath, err := parseExtractArchiveArgs(args)
	if err != nil {
		return "", err
	}
	prof := tctx.ProfileID()
	format, err := archiveFormat(archivePath)
	if err != nil {
		return "", err
	}
	archive, relArchive, err := t.loadArchive(ctx, tctx, archivePath, prof)
	if err != nil {
		return "", err
	}
	dest, relDest, err := t.prepareDest(ctx, tctx, destPath, prof)
	if err != nil {
		return "", err
	}
	budget := &extractBudget{out: make([]extractEntryResult, 0, 16)}
	guard := entryGuard(protectedEntryGuard)
	sink := extractSink{ctx: ctx, tctx: tctx, dest: resolvedMutationTarget(dest)}
	switch format {
	case "zip":
		err = extractZipFile(sink, archive.EffectLocation(), budget, guard)
	case "tar.gz":
		err = extractTarGzFile(sink, archive.EffectLocation(), budget, guard)
	default:
		err = &tools.ToolReject{
			Code: "EXTRACT_FORMAT_UNSUPPORTED",
			Data: map[string]any{"path": relArchive},
		}
	}
	if err != nil {
		afterSuccessfulMutation(ctx, tctx, relDest)
		return "", extractionFailure(err, relArchive, relDest, budget)
	}
	resp := extractArchiveResponse{
		Path:      relArchive,
		Dest:      relDest,
		Extracted: budget.out,
		Entries:   budget.entries,
		Bytes:     budget.bytes,
	}
	mutPaths := make([]string, 0, 1+len(budget.out))
	mutPaths = append(mutPaths, relDest)
	for _, e := range budget.out {
		if p := strings.TrimSpace(e.Path); p != "" {
			mutPaths = append(mutPaths, filepath.ToSlash(filepath.Join(relDest, p)))
		}
	}
	afterSuccessfulMutation(ctx, tctx, mutPaths...)
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Failed extraction retains the paths and byte counts already committed.
func extractionFailure(cause error, archive, dest string, budget *extractBudget) error {
	reject := tools.ToolReject{Code: tools.ToolOwnerFailedCode}
	var original *tools.ToolReject
	if errors.As(cause, &original) {
		reject = *original
	}
	data := make(map[string]any, len(reject.Data)+6)
	for key, value := range reject.Data {
		data[key] = value
	}
	if _, present := data["path"]; !present {
		data["path"] = archive
	}
	data["dest"] = dest
	data["extracted_entries"], data["extracted_bytes"] = budget.entries, budget.bytes
	paths := make([]string, 0, len(budget.out))
	for _, entry := range budget.out {
		paths = append(paths, filepath.ToSlash(filepath.Join(dest, entry.Path)))
	}
	data["extracted_paths"] = paths
	sample := extractionPathSample(paths)
	data["extracted_paths_sample"] = sample
	data["extracted_paths_omitted"] = len(paths) - len(sample)
	data["extraction_started"] = true
	if original == nil {
		data["reason"] = cause.Error()
	}
	reject.Data = data
	return errors.Join(&reject, cause)
}

// Recovery copy uses a bounded sample of the complete structured paths.
func extractionPathSample(paths []string) []string {
	const maxPaths, maxBytes = 10, 2048
	sample := make([]string, 0, min(len(paths), maxPaths))
	used := 0
	for _, path := range paths {
		if len(sample) == maxPaths || used+len(path) > maxBytes {
			break
		}
		sample = append(sample, path)
		used += len(path)
	}
	return sample
}

func parseExtractArchiveArgs(args map[string]any) (path, dest string, err error) {
	path, _ = args["path"].(string)
	dest, _ = args["dest"].(string)
	path = strings.TrimSpace(path)
	dest = strings.TrimSpace(dest)
	if path == "" {
		return "", "", toolkit.MissingArg("path")
	}
	if dest == "" {
		return "", "", toolkit.MissingArg("dest")
	}
	return path, dest, nil
}

func (t *ExtractArchiveTool) loadArchive(ctx context.Context, tctx tools.ToolContext, relPath, profileID string) (projectpaths.Resolved, string, error) {
	if sandbox.HasParentTraversal(relPath) {
		return projectpaths.Resolved{}, "", toolkit.PathEscapeReject(relPath)
	}
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, relPath)
	if err != nil {
		return projectpaths.Resolved{}, "", err
	}
	relSlash := filepath.ToSlash(resolved.DisplayPath)
	f, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		if os.IsNotExist(err) {
			return projectpaths.Resolved{}, "", &tools.ToolReject{
				Code: "EXTRACT_NOT_FOUND",
				Data: map[string]any{"path": relSlash},
			}
		}
		return projectpaths.Resolved{}, "", fmt.Errorf("extract_archive %s: %w", relPath, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return projectpaths.Resolved{}, "", err
	}
	if info.IsDir() {
		return projectpaths.Resolved{}, "", &tools.ToolReject{
			Code: "EXTRACT_IS_DIRECTORY",
			Data: map[string]any{"path": relSlash},
		}
	}
	return resolved, relSlash, nil
}

func (t *ExtractArchiveTool) prepareDest(ctx context.Context, tctx tools.ToolContext, relPath, profileID string) (projectpaths.Resolved, string, error) {
	if sandbox.HasParentTraversal(relPath) {
		return projectpaths.Resolved{}, "", toolkit.PathEscapeReject(relPath)
	}
	relSlash := filepath.ToSlash(relPath)
	resolved, err := assertExtractWritePath(ctx, t.Boundary, tctx, relPath, profileID)
	if err != nil {
		return projectpaths.Resolved{}, "", err
	}
	fullPath := resolved.Abs
	info, err := os.Lstat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(resolved), 0o755); err != nil {
				return projectpaths.Resolved{}, "", fmt.Errorf("extract dest %s: %w", relPath, err)
			}
			return resolved, relSlash, nil
		}
		return projectpaths.Resolved{}, "", fmt.Errorf("extract dest %s: %w", relPath, err)
	}
	if !info.IsDir() {
		return projectpaths.Resolved{}, "", &tools.ToolReject{
			Code: "EXTRACT_DEST_NOT_DIR",
			Data: map[string]any{"dest": relSlash},
		}
	}
	return resolved, relSlash, nil
}

// protectedEntryGuard rejects governed archive destinations.
func protectedEntryGuard(relEntry, absTarget string) error {
	if class, denied := tools.IsGitInternalsWritePath(relEntry); denied {
		return &tools.ToolReject{
			Code: "GIT_INTERNALS_WRITE_DENIED",
			Data: map[string]any{
				"path":  filepath.ToSlash(relEntry),
				"class": class,
			},
		}
	}
	return nil
}

func assertExtractWritePath(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	relPath, profileID string,
) (projectpaths.Resolved, error) {
	return assertWritePath(ctx, boundary, tctx, relPath, profileID, "extract_archive", func(path string) error {
		return &tools.ToolReject{Code: "EXTRACT_PATH_DENIED", Data: map[string]any{"path": path}}
	})
}
