package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CodeRewriteTool applies structural edits through the workspace write gate.
type CodeRewriteTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

func (t *CodeRewriteTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	var overrideErr error
	ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
	if overrideErr != nil {
		return "", overrideErr
	}
	path, _ := args["path"].(string)
	pattern, _ := args["pattern"].(string)
	rewrite, _ := args["rewrite"].(string)
	lang, _ := args["lang"].(string)
	dryRun, _ := args["dry_run"].(bool)
	if dryRun {
		// Previews report diagnostics even when a later mutation may override them.
		ctx = syntaxhealth.WithOverride(ctx, "")
	}

	if strings.TrimSpace(pattern) == "" {
		return "", sourceview.PatternInvalid(anchorOrDot(path, args), errors.New("pattern is required"))
	}
	if strings.TrimSpace(rewrite) == "" {
		return "", sourceview.PatternInvalid(anchorOrDot(path, args), errors.New("rewrite is required; use grep structural:true to search"))
	}

	anchor := anchorOrDot(path, args)
	if op, tooBroad := structrewrite.BareBinaryMetavarOp(ctx, lang, anchor, pattern); tooBroad {
		return "", codeRewritePatternTooBroad(anchor, op, pattern)
	}

	if paths, hasPaths := parseRewritePaths(args); hasPaths {
		return t.runMultiApply(ctx, args, tctx, paths, pattern, rewrite, lang, dryRun)
	}

	if strings.TrimSpace(path) == "" {
		return "", toolkit.MissingArg("path")
	}
	return t.runSingleApply(ctx, tctx, path, pattern, rewrite, lang, dryRun)
}

func (t *CodeRewriteTool) runSingleApply(ctx context.Context, tctx tools.ToolContext, path, pattern, rewrite, lang string, dryRun bool) (string, error) {
	if _, ok := structrewrite.SupportedLanguage(lang, path); !ok {
		if strings.TrimSpace(lang) != "" {
			return "", sourceview.LanguageUnknown(path, lang)
		}
		return codeRewriteUnsupported(path), nil
	}

	var resolved projectpaths.Resolved
	if dryRun {
		var err error
		resolved, err = projectpaths.ResolveRead(ctx, t.Boundary, tctx, path)
		if err != nil {
			return "", err
		}
	} else {
		if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "code_rewrite"); err != nil {
			return "", writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "code_rewrite", err)
		}
		if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
			return "", err
		}
		var err error
		resolved, err = projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
		if err != nil {
			return "", err
		}
	}

	fullPath := resolved.Abs
	path = resolved.DisplayPath
	return retryEditorDocument(ctx, path, func() (string, error) {
		st, err := loadRewriteSource(ctx, tctx, resolved, dryRun)
		if err != nil {
			if os.IsNotExist(err) {
				return "", sourceview.PathNotFound("code_rewrite", path, fullPath)
			}
			return "", fmt.Errorf("read failed: %w", err)
		}
		res, err := structrewrite.Run(ctx, structrewrite.Request{
			LangName: lang,
			Filename: path,
			Source:   []byte(st.Content),
			Pattern:  pattern,
			Fix:      rewrite,
		})
		if err != nil {
			if reject := sourceview.ParseReject(path, "source", err); reject != nil {
				return "", reject
			}
			var invalid *structrewrite.PatternError
			if errors.As(err, &invalid) {
				return "", sourceview.PatternInvalid(path, err)
			}
			return "", &tools.ToolReject{Code: "STRUCTURAL_APPLY_FAILED", Data: map[string]any{"path": path, "reason": err.Error()}}
		}
		if !res.Changed {
			return fmt.Sprintf("No matches for pattern in %s; file unchanged", path), nil
		}

		finalContent := string(res.Rewritten)
		if err := guardMutationContent("code_rewrite", path, finalContent); err != nil {
			return "", err
		}
		if dryRun {
			return codeRewriteDiffResult(ctx, path, res.Language, len(res.Matches), st.Content, finalContent), nil
		}

		reportTextIntent(tctx, resolved, st, true, finalContent)
		if t.ContentApply != nil {
			before := st.Content
			proposed := finalContent
			var gateErr error
			finalContent, gateErr = t.ContentApply.GateApply(ctx, "code_rewrite", path, &before, finalContent, tctx)
			if gateErr != nil {
				return "", gateErr
			}
			if finalContent != proposed {
				reportTextIntent(tctx, resolved, st, true, finalContent)
			}
		}
		beforeText := st.Content
		if err := rejectIfSyntaxUnhealthy(ctx, "code_rewrite", path, &beforeText, finalContent, mutationSeam{}); err != nil {
			return "", err
		}
		landed, err := landEditedText(ctx, tctx, "code_rewrite", resolved, st, finalContent)
		if err != nil {
			return "", err
		}
		beforeCopy := st.Content
		captureFileEdit(tctx, path, finalContent, &beforeCopy)
		afterSuccessfulMutation(ctx, tctx, path)
		receipt := fmt.Sprintf("Rewrote %d match(es) in %s [%s]", len(res.Matches), path, res.Language)
		if note := landed.note(); note != "" {
			receipt += "\n" + note
		}
		return receipt, nil
	})
}

type codeRewriteDiffOut struct {
	SyntaxIssue *rewriteBlocked `json:"syntax_issue,omitempty"`
	Path        string          `json:"path"`
	Language    string          `json:"language"`
	DryRun      bool            `json:"dry_run"`
	Matches     int             `json:"matches"`
	Diff        string          `json:"diff"`
	ParseError  string          `json:"parse_error,omitempty"`
}

func codeRewriteDiffResult(ctx context.Context, path, language string, matches int, before, after string) string {
	out := codeRewriteDiffOut{
		Path:     path,
		Language: language,
		DryRun:   true,
		Matches:  matches,
		Diff:     sourceview.UnifiedDiff(path, path, sourceview.SplitLines(before), sourceview.SplitLines(after), 3),
	}
	beforeCopy := before
	if err := rejectIfSyntaxUnhealthy(ctx, "code_rewrite", path, &beforeCopy, after, mutationSeam{}); err != nil {
		var reject *tools.ToolReject
		if errors.As(err, &reject) {
			out.SyntaxIssue = &rewriteBlocked{Path: path, Code: reject.Code, Details: reject.Data}
			out.ParseError, _ = reject.Data["parse_error"].(string)
			if out.ParseError == "" {
				out.ParseError, _ = reject.Data["parse_failure"].(string)
				if out.ParseError == "" {
					out.ParseError = fmt.Sprint(reject.Data["parse_status"])
				}
			}
		}
	}
	b, err := surveyjson.Marshal(out)
	if err != nil {
		return fmt.Sprintf("dry-run diff for %s unavailable: %v", path, err)
	}
	return string(b)
}

func codeRewritePatternTooBroad(path, operator, pattern string) error {
	return &tools.ToolReject{
		Code: "CODE_REWRITE_PATTERN_TOO_BROAD",
		Data: map[string]any{
			"path":     path,
			"operator": operator,
			"pattern":  pattern,
		},
	}
}

func codeRewriteUnsupported(path string) string {
	return fmt.Sprintf("code_rewrite skipped: no tree-sitter grammar for %s; pass lang or use edit", path)
}

func anchorOrDot(path string, args map[string]any) string {
	if anchor := strings.TrimSpace(path); anchor != "" {
		return anchor
	}
	if paths, ok := parseRewritePaths(args); ok && len(paths) > 0 {
		return paths[0]
	}
	return "."
}

// loadRewriteSource holds a dry run to the mutation budget too, so a preview
// never offers a rewrite the apply would refuse.
func loadRewriteSource(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, dryRun bool) (sourceview.Text, error) {
	if dryRun {
		return sourceview.LoadText(ctx, "code_rewrite", sourceview.AccessMutate, tctx, resolved)
	}
	return loadAgentSourceText(ctx, "code_rewrite", tctx, resolved)
}
