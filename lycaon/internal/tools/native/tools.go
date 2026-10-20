package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

const coordinatorProfileID = "coordinator"

// WriteTool writes file contents atomically within the sandbox.
type WriteTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

// ContentApplyGate holds a proposed write for review when policy requires it and returns the content to write.
type ContentApplyGate interface {
	GateApply(ctx context.Context, tool, path string, before *string, after string, tctx tools.ToolContext) (string, error)
}

func (t *WriteTool) Name() string { return "write" }

func (t *WriteTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	ctx, err := tools.WithSyntaxOverride(ctx, args)
	if err != nil {
		return "", err
	}
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	appendMode, _ := args["append"].(bool)
	result, err := t.Apply(ctx, WriteInput{Path: path, Content: content, Append: appendMode}, tctx)
	if err != nil {
		return "", err
	}
	return result.Receipt(), nil
}

// EditTool replaces one occurrence of old_string with new_string.
type EditTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	var overrideErr error
	ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
	if overrideErr != nil {
		return "", overrideErr
	}
	path, _ := args["path"].(string)
	oldString, _ := args["old_string"].(string)
	newString, _ := args["new_string"].(string)
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if oldString == "" {
		return "", toolkit.MissingArg("old_string")
	}
	if _, hasNew := args["new_string"]; !hasNew {
		return "", toolkit.MissingArg("new_string")
	}
	if oldString == newString {
		return "", editArgsConflict("old_string and new_string must differ")
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "edit"); err != nil {
		return "", writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "edit", err)
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return "", err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", err
	}
	fullPath := resolved.Abs
	path = resolved.DisplayPath
	replaceAll := toolkit.BoolArg(args, "replace_all", false)
	return retryEditorOperation(ctx, path, func() (string, error) {
		st, err := loadAgentSourceText(ctx, "edit", tctx, resolved)
		if err != nil {
			if os.IsNotExist(err) {
				return "", sourceview.PathNotFound("edit", path, fullPath)
			}
			return "", fmt.Errorf("read failed: %w", err)
		}
		old := st.Content
		occurrences := strings.Count(old, oldString)
		switch {
		case occurrences == 0:
			if changes, total, ok := sourceview.ForeignChanges(ctx, tctx, fullPath); ok {
				return "", editTargetChangedByOthers(path, old, oldString, newString, changes, total)
			}
			return "", editOldStringNotFound(path, old, oldString, newString)
		case occurrences > 1 && !replaceAll:
			return "", editOldStringAmbiguous(path, occurrences)
		}
		replaced := 1
		newContent := strings.Replace(old, oldString, newString, 1)
		// Multiple replacements have disjoint seams; zero selects the first diagnostic.
		seamStartLine := strings.Count(old[:strings.Index(old, oldString)], "\n") + 1 //nolint:gocritic // the occurrences == 0 case returned above, so Index cannot be -1
		if replaceAll {
			replaced = occurrences
			newContent = strings.ReplaceAll(old, oldString, newString)
			if occurrences > 1 {
				seamStartLine = 0
			}
		}
		return t.finishEdit(ctx, path, resolved, st, old, newContent, tctx, replaced, len(oldString), len(newString), seamStartLine, toolkit.CountLines(newString))
	})
}

func captureFileEdit(tctx tools.ToolContext, path, after string, before *string) {
	if tctx.Effects.Out == nil || strings.TrimSpace(path) == "" {
		return
	}
	// The recorded edit names each value this call resolved by its reference.
	if before != nil {
		referenced := tctx.Effects.Secrets.ReferenceEchoes(*before)
		before = &referenced
	}
	tctx.Effects.Out.FileEdit = &tools.FileEditCapture{
		Path:   path,
		Before: before,
		After:  tctx.Effects.Secrets.ReferenceEchoes(after),
	}
}

// verifyTextWriteBase compares the destination immediately before replacement.
// An empty hash is the new-file expectation and refuses a path created after
// resolution; a hash refuses any byte change since the validated snapshot,
// which was loaded within the mutation budget.
func verifyTextWriteBase(target fseffect.Target, fullPath, baseSHA256 string) error {
	if baseSHA256 == "" {
		if _, err := target.Lstat(); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("stat write base: %w", err)
		}
		return &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath, "text_base_changed": true}}
	}
	currentFile, err := target.Open()
	if err != nil {
		return &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath}}
	}
	defer func() { _ = currentFile.Close() }()
	info, err := currentFile.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > readcaps.MaxMutationBytes {
		return &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath}}
	}
	current, err := io.ReadAll(io.LimitReader(currentFile, readcaps.MaxMutationBytes+1))
	if err != nil {
		return &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath}}
	}
	if textfile.SHA256(current) != baseSHA256 {
		return &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath, "text_base_changed": true}}
	}
	return nil
}
