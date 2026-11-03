package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

func codeRewritePatternTooBroad(path, operator, pattern string) error {
	return &toolrejection.ToolReject{
		Code: "CODE_REWRITE_PATTERN_TOO_BROAD",
		Data: map[string]any{
			"path":     path,
			"operator": operator,
			"pattern":  pattern,
		},
	}
}

// SupportsRewriteLanguage reports whether path has a tree-sitter grammar for code-rewrite.
func SupportsRewriteLanguage(path string) bool {
	_, ok := structrewrite.SupportedLanguage("", path)
	return ok
}

// RewriteSource applies a structural AST pattern rewrite to content.
func RewriteSource(ctx context.Context, path, pattern, rewrite, content string) (string, bool, error) {
	if op, tooBroad := structrewrite.BareBinaryMetavarOp(ctx, "", path, pattern); tooBroad {
		return "", false, codeRewritePatternTooBroad(path, op, pattern)
	}
	if _, ok := structrewrite.SupportedLanguage("", path); !ok {
		return content, false, nil
	}
	res, err := structrewrite.Run(ctx, structrewrite.Request{
		Filename: path,
		Source:   []byte(content),
		Pattern:  pattern,
		Fix:      rewrite,
	})
	if err != nil {
		if reject := sourceview.ParseReject(path, "source", err); reject != nil {
			return "", false, reject
		}
		var invalid *structrewrite.PatternError
		if errors.As(err, &invalid) {
			return "", false, sourceview.PatternInvalid(path, err)
		}
		return "", false, &toolrejection.ToolReject{Code: "STRUCTURAL_APPLY_FAILED", Data: map[string]any{"path": path, "reason": err.Error()}}
	}
	if !res.Changed {
		return content, false, nil
	}
	rewritten := string(res.Rewritten)
	if err := guardMutationContent("code_rewrite", path, rewritten); err != nil {
		return "", false, err
	}
	return rewritten, true, nil
}
