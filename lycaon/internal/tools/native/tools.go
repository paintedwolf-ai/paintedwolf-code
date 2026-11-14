package native

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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
	result, err := t.Apply(ctx, WriteInput{Path: path, Content: content, Append: appendMode, Admission: WriteConversational}, tctx)
	if err != nil {
		return "", err
	}
	return result.Receipt(), nil
}

func captureFileEdit(tctx tools.ToolContext, resolved projectpaths.Resolved, path, after string, before *string) {
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
		RootID: resolved.Root.ID,
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
