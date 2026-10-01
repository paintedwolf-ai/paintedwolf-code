package native

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// JqEditTool writes the result of a jq program over a structured document,
// in place or to dest, through the same write door as write and edit.
type JqEditTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

func (t *JqEditTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	const tool = nativejq.EditToolName
	path := strings.TrimSpace(stringArgOrEmpty(args, "path"))
	query := strings.TrimSpace(stringArgOrEmpty(args, "query"))
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if query == "" {
		return "", toolkit.MissingArg("query")
	}
	dest := strings.TrimSpace(stringArgOrEmpty(args, "dest"))
	targetPath := path
	if dest != "" {
		targetPath = dest
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, targetPath, tool); err != nil {
		return "", writeScopeReject(ctx, t.Boundary, targetPath, tctx.ProfileID(), tool, err)
	}
	if err := beforeWorkerMutation(ctx, tctx, targetPath); err != nil {
		return "", err
	}
	target, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, targetPath)
	if err != nil {
		return "", err
	}
	source := target
	if dest != "" {
		if source, err = projectpaths.ResolveRead(ctx, t.Boundary, tctx, path); err != nil {
			return "", err
		}
	}
	return retryEditorDocument(ctx, target.DisplayPath, func() (string, error) {
		src, err := t.loadSource(ctx, tctx, source, dest != "")
		if err != nil {
			return "", err
		}
		rawVars, _ := args["vars"].(map[string]any)
		edited, err := nativejq.Edit(ctx, nativejq.EditRequest{
			Path: source.DisplayPath, Format: stringArgOrEmpty(args, "format"), Query: query, Text: src.Content, Vars: rawVars,
		})
		if err != nil {
			return "", err
		}
		st, before := src, &src.Content
		if dest != "" {
			if st, before, err = loadWriteBase(ctx, tctx, target); err != nil {
				return "", err
			}
		}
		if before != nil && *before == edited.Text {
			return fmt.Sprintf("No change: the %s result equals %s", edited.Format, target.DisplayPath), nil
		}
		return t.land(ctx, tctx, target, st, before, edited)
	})
}

// loadSource reads the document the program runs over. An in-place edit reads
// the mutation base; a separate source is an ordinary read.
func (t *JqEditTool) loadSource(ctx context.Context, tctx tools.ToolContext, source projectpaths.Resolved, separate bool) (sourceview.Text, error) {
	var st sourceview.Text
	var err error
	if separate {
		st, err = sourceview.LoadText(ctx, nativejq.EditToolName, sourceview.AccessRead, tctx, source)
	} else {
		st, err = loadAgentSourceText(ctx, nativejq.EditToolName, tctx, source)
	}
	if os.IsNotExist(err) {
		return sourceview.Text{}, sourceview.PathNotFound(nativejq.EditToolName, source.DisplayPath, source.Abs)
	}
	if err != nil {
		return sourceview.Text{}, err
	}
	if separate {
		tctx.RecordSourcePath(source.Abs, api.NavigationEntryKindFile)
	}
	return st, nil
}

// loadWriteBase reads a separate destination; a missing one is a create.
func loadWriteBase(ctx context.Context, tctx tools.ToolContext, target projectpaths.Resolved) (sourceview.Text, *string, error) {
	st, err := loadAgentSourceText(ctx, nativejq.EditToolName, tctx, target)
	if os.IsNotExist(err) {
		return sourceview.Text{}, nil, nil
	}
	if err != nil {
		return sourceview.Text{}, nil, fmt.Errorf("read failed: %w", err)
	}
	return st, &st.Content, nil
}

func (t *JqEditTool) land(ctx context.Context, tctx tools.ToolContext, target projectpaths.Resolved, st sourceview.Text, before *string, edited nativejq.EditResult) (string, error) {
	const tool = nativejq.EditToolName
	path := target.DisplayPath
	finalContent := edited.Text
	if err := guardMutationContent(tool, path, finalContent); err != nil {
		return "", err
	}
	reportTextIntent(tctx, target, st, before != nil, finalContent)
	if t.ContentApply != nil {
		gated, err := t.ContentApply.GateApply(ctx, tool, path, before, finalContent, tctx)
		if err != nil {
			return "", err
		}
		if gated != finalContent {
			reportTextIntent(tctx, target, st, before != nil, gated)
		}
		finalContent = gated
	}
	landed, err := landEditedText(ctx, tctx, tool, target, st, finalContent)
	if err != nil {
		return "", err
	}
	captureFileEdit(tctx, path, finalContent, before)
	afterSuccessfulMutation(ctx, tctx, path)
	receipt := fmt.Sprintf("Wrote %d %s document(s) to %s (%d bytes)", edited.Documents, edited.Format, path, len(finalContent))
	if note := landed.note(); note != "" {
		receipt += "\n" + note
	}
	return receipt, nil
}
