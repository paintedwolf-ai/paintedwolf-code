package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CopyTool copies regular files, and bounded directory trees, within write scope.
type CopyTool struct {
	Boundary *sandbox.Boundary
	// Caps overrides the catalog recursive mutation ceilings; zero uses them.
	Caps RecursiveCaps
}

type copyResult struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Bytes int64  `json:"bytes"`
}

type copyResponse struct {
	Copied []copyResult `json:"copied"`
}

// CopyInput is one complete copy operation. Recursive copies a directory tree
// parents first, measured against the catalog's recursive mutation caps before
// any entry is written. NoClobber keeps existing destination files.
type CopyInput struct {
	Pairs     []FilePair
	Recursive bool
	NoClobber bool
	// Preserve copies permission bits and access/modification times.
	Preserve     bool
	MaxFileBytes int64
}

// CopyResult lists written files in order, including those already written
// when a later entry fails.
type CopyResult struct {
	Copied  []copyResult
	Skipped []FilePair
}

func (t *CopyTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (output string, runErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pairs, maxBytes, err := parseCopyPairs(args)
	if err != nil {
		return "", err
	}
	result, err := t.Copy(ctx, CopyInput{Pairs: pairs, MaxFileBytes: maxBytes}, tctx)
	resp := copyResponse{Copied: result.Copied}
	if resp.Copied == nil {
		resp.Copied = []copyResult{}
	}
	if err != nil {
		if len(resp.Copied) > 0 {
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

// Copy writes each destination through the reviewed, recoverable stream door.
func (t *CopyTool) Copy(ctx context.Context, in CopyInput, tctx tools.ToolContext) (CopyResult, error) {
	var result CopyResult
	caps, err := t.Caps.resolve()
	if err != nil {
		return result, err
	}
	maxBytes := in.MaxFileBytes
	if maxBytes <= 0 {
		if in.Recursive {
			maxBytes = caps.Bytes
		} else {
			maxBytes = defaultCopyMaxBytes
		}
	}
	prof := tctx.ProfileID()
	for _, pair := range in.Pairs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if in.Recursive {
			source, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.From, prof)
			if err != nil {
				return result, err
			}
			if info, err := os.Lstat(source.Abs); err == nil && info.IsDir() {
				if err := t.copyTree(ctx, tctx, source, pair, in, caps, &result); err != nil {
					return result, err
				}
				continue
			}
		}
		if err := t.copyFile(ctx, tctx, pair, in, maxBytes, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (t *CopyTool) copyFile(ctx context.Context, tctx tools.ToolContext, pair FilePair, in CopyInput, maxBytes int64, result *CopyResult) error {
	prof := tctx.ProfileID()
	fromSlash, toSlash := filepath.ToSlash(pair.From), filepath.ToSlash(pair.To)
	srcResolved, err := assertMutationFileSource(ctx, t.Boundary, tctx, pair.From, prof, "copy",
		"COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
	if err != nil {
		return err
	}
	dstResolved, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.To, prof)
	if err != nil {
		return err
	}
	if in.NoClobber {
		if _, err := os.Lstat(dstResolved.Abs); err == nil {
			result.Skipped = append(result.Skipped, FilePair{From: fromSlash, To: toSlash})
			return nil
		}
	}
	n, err := copyFileWithinLimit(ctx, tctx, resolvedMutationTarget(srcResolved), resolvedMutationTarget(dstResolved), maxBytes)
	if err != nil {
		reject := &toolrejection.ToolReject{}
		if errors.As(err, &reject) {
			return reject
		}
		return fmt.Errorf("copy %s -> %s: %w", pair.From, pair.To, err)
	}
	result.Copied = append(result.Copied, copyResult{From: fromSlash, To: toSlash, Bytes: n})
	afterSuccessfulMutation(ctx, tctx, toSlash)
	if in.Preserve {
		return preserveMetadata(ctx, tctx, srcResolved.Abs, resolvedMutationTarget(dstResolved))
	}
	return nil
}

// preserveMetadata copies permission bits and times from source to target
// through the reviewed metadata path. A platform without timestamp support
// refuses rather than leaving -p half applied silently.
func preserveMetadata(ctx context.Context, tctx tools.ToolContext, source string, target mutationTarget) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := chmodAgentPath(ctx, tctx, target, info.Mode().Perm()); err != nil {
		return err
	}
	modify := info.ModTime()
	var access *time.Time
	if at, ok := fseffect.AccessTime(info); ok {
		access = &at
	}
	if err := timesAgentPath(ctx, tctx, target, access, &modify); err != nil {
		if errors.Is(err, fseffect.ErrUnsupported) {
			return &toolrejection.ToolReject{Code: "COMMAND_PLATFORM_UNAVAILABLE", Data: map[string]any{"path": target.Abs, "capability": "timestamps"}}
		}
		return err
	}
	return nil
}

// copyTree recreates a directory tree at the destination, parents first.
// Copying into a new destination streams files with bounded memory and no
// per-entry recovery overhead.
func (t *CopyTool) copyTree(ctx context.Context, tctx tools.ToolContext, source projectpaths.Resolved, pair FilePair, in CopyInput, caps RecursiveCaps, result *CopyResult) error {
	prof := tctx.ProfileID()
	dstRoot, err := assertCopyWritePath(ctx, t.Boundary, tctx, pair.To, prof)
	if err != nil {
		return err
	}
	_, dstStatErr := os.Lstat(dstRoot.Abs)
	dstIsNew := os.IsNotExist(dstStatErr)

	type treeEntry struct {
		absPath string
		rel     string
		isDir   bool
		mode    os.FileMode
		srcLoc  fseffect.Location
	}
	var order []treeEntry
	var files, bytes int64
	hasPolicy := false
	checkPins := len(tctx.Turn.TurnWritePinGlobs) > 0
	fromSlash := filepath.ToSlash(pair.From)

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
			rel := child.Name()
			if relDir != "." && relDir != "" {
				rel = filepath.Join(relDir, child.Name())
			}
			info, err := child.Info()
			if err != nil {
				info, err = os.Lstat(full)
				if err != nil {
					return err
				}
			}
			dstAbs := filepath.Join(dstRoot.Abs, rel)
			dstScopeRel := filepath.ToSlash(filepath.Join(dstRoot.ScopeRel, rel))
			srcScopeRel := filepath.ToSlash(filepath.Join(source.ScopeRel, rel))
			if _, policy := tctx.AgentPolicyTarget(dstAbs); policy {
				hasPolicy = true
			}
			_, deniedDstGit := tools.IsGitInternalsWritePath(dstScopeRel)
			_, deniedSrcGit := tools.IsGitInternalsWritePath(srcScopeRel)
			if checkPins || hasPolicy || deniedDstGit || tools.IsSensitivePath(dstScopeRel) {
				if _, err := assertCopyWritePath(ctx, t.Boundary, tctx, filepath.Join(pair.To, rel), prof); err != nil {
					return err
				}
			}
			if deniedSrcGit || tools.IsSensitivePath(srcScopeRel) {
				if _, err := assertCopyWritePath(ctx, t.Boundary, tctx, full, prof); err != nil {
					return err
				}
			}
			if info.IsDir() {
				order = append(order, treeEntry{absPath: full, rel: rel, isDir: true, mode: info.Mode()})
				if err := walk(full, rel); err != nil {
					return err
				}
				continue
			}
			files++
			var srcLoc fseffect.Location
			if info.Mode().IsRegular() {
				bytes += info.Size()
			} else if info.Mode()&os.ModeSymlink != 0 {
				targetInfo, statErr := os.Stat(full)
				if statErr == nil && targetInfo.Mode().IsRegular() {
					bytes += targetInfo.Size()
				}
				resolvedSrc, err := assertMutationFileSource(ctx, t.Boundary, tctx, full, prof, "copy",
					"COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
				if err != nil {
					return err
				}
				srcLoc = resolvedSrc.EffectLocation()
			}
			if files > caps.Files {
				return recursiveCapRefusal("recursive_mutation_files", "files", caps.Files, files, fromSlash)
			}
			if bytes > caps.Bytes {
				return recursiveCapRefusal("recursive_mutation_bytes", "bytes", caps.Bytes, bytes, fromSlash)
			}
			order = append(order, treeEntry{absPath: full, rel: rel, isDir: false, mode: info.Mode(), srcLoc: srcLoc})
		}
		return nil
	}
	if err := walk(source.Abs, "."); err != nil {
		return err
	}

	if dstIsNew && !hasPolicy {
		if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(dstRoot), 0o755); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.ToSlash(pair.To), err)
		}
		srcScope, err := os.OpenRoot(source.Abs)
		if err != nil {
			return err
		}
		defer func() { _ = srcScope.Close() }()
		dstScope, err := os.OpenRoot(dstRoot.Abs)
		if err != nil {
			return err
		}
		defer func() { _ = dstScope.Close() }()
		for _, e := range order {
			if err := ctx.Err(); err != nil {
				return err
			}
			toRel := filepath.Join(pair.To, e.rel)
			dstLoc := fseffect.Location{Root: dstRoot.EffectLocation().Root, Rel: filepath.Join(dstRoot.EffectLocation().Rel, e.rel)}
			if e.isDir {
				if err := dstScope.Mkdir(e.rel, 0o755); err != nil && !os.IsExist(err) {
					return fmt.Errorf("copy %s: %w", filepath.ToSlash(toRel), err)
				}
				continue
			}
			n, err := streamCopyEntryToScope(srcScope, dstScope, e.rel, e.srcLoc, e.mode)
			if err != nil {
				return fmt.Errorf("copy %s -> %s: %w", e.absPath, toRel, err)
			}
			if in.Preserve {
				if err := preserveMetadataDirect(e.absPath, dstLoc); err != nil {
					return err
				}
			}
			result.Copied = append(result.Copied, copyResult{
				From:  filepath.ToSlash(filepath.Join(pair.From, e.rel)),
				To:    filepath.ToSlash(toRel),
				Bytes: n,
			})
		}
		if in.Preserve {
			for i := len(order) - 1; i >= 0; i-- {
				if order[i].isDir {
					dstLoc := fseffect.Location{Root: dstRoot.EffectLocation().Root, Rel: filepath.Join(dstRoot.EffectLocation().Rel, order[i].rel)}
					if err := preserveMetadataDirect(order[i].absPath, dstLoc); err != nil {
						return err
					}
				}
			}
			if err := preserveMetadataDirect(source.Abs, dstRoot.EffectLocation()); err != nil {
				return err
			}
		}
		afterSuccessfulMutation(ctx, tctx, filepath.ToSlash(pair.To))
		return nil
	}

	if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(dstRoot), 0o755); err != nil {
		return fmt.Errorf("copy %s: %w", filepath.ToSlash(pair.To), err)
	}
	for _, e := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		to := filepath.Join(pair.To, e.rel)
		if !e.isDir {
			if err := t.copyFile(ctx, tctx, FilePair{From: e.absPath, To: to}, in, caps.Bytes, result); err != nil {
				return err
			}
			continue
		}
		target, err := assertCopyWritePath(ctx, t.Boundary, tctx, to, prof)
		if err != nil {
			return err
		}
		if err := mkdirAgentPath(ctx, tctx, resolvedMutationTarget(target), 0o755); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.ToSlash(to), err)
		}
	}
	return nil
}

func streamCopyEntryToScope(srcScope, dstScope *os.Root, rel string, symlinkLoc fseffect.Location, mode os.FileMode) (int64, error) {
	var in *os.File
	var err error
	if mode&os.ModeSymlink != 0 {
		in, err = fseffect.OpenRead(symlinkLoc)
	} else {
		in, err = srcScope.Open(rel)
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := dstScope.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	return n, closeErr
}

func preserveMetadataDirect(source string, dstLoc fseffect.Location) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if _, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
		Location: dstLoc,
		Update:   func(os.FileMode) os.FileMode { return info.Mode().Perm() },
	}); err != nil {
		return err
	}
	modify := info.ModTime()
	var access *time.Time
	if at, ok := fseffect.AccessTime(info); ok {
		access = &at
	}
	if _, err := fseffect.ChangeTimes(fseffect.TimesRequest{Location: dstLoc, Access: access, Modify: &modify}); err != nil {
		if errors.Is(err, fseffect.ErrUnsupported) {
			return &toolrejection.ToolReject{Code: "COMMAND_PLATFORM_UNAVAILABLE", Data: map[string]any{"path": filepath.Join(dstLoc.Root, dstLoc.Rel), "capability": "timestamps"}}
		}
		return err
	}
	return nil
}

func copyFileWithinLimit(ctx context.Context, tctx tools.ToolContext, source, target mutationTarget, maxBytes int64) (int64, error) {
	in, err := fseffect.OpenRead(source.Location)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > maxBytes {
		return 0, &toolrejection.ToolReject{
			Code: "COPY_SIZE_EXCEEDED",
			Data: map[string]any{
				"max_file_bytes": maxBytes,
				"size":           info.Size(),
			},
		}
	}
	readLimit := maxBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	result, err := applyAgentStream(ctx, tctx, agentStreamRequest{
		Target: target, Source: io.LimitReader(in, readLimit),
		BeforeCommit: func(_ fseffect.Target, staged fseffect.Result) error {
			if staged.Bytes <= maxBytes {
				return nil
			}
			return &toolrejection.ToolReject{
				Code: "COPY_SIZE_EXCEEDED",
				Data: map[string]any{
					"max_file_bytes": maxBytes,
					"size":           staged.Bytes,
				},
			}
		},
	})
	if err != nil {
		return 0, err
	}
	return result.Bytes, nil
}
