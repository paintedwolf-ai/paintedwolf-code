package mutation

import (
	"os"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func editReadRequired(path, docID string) error {
	return &toolrejection.ToolReject{
		Code: "EDITOR_DOCUMENT_READ_REQUIRED",
		Data: map[string]any{"path": path, "document_id": docID},
	}
}

func editCoversMatches(source, oldString string, replaceAll bool, editor *tools.EditorDocumentText) bool {
	if editor == nil || editor.ReadWhole {
		return true
	}
	offset := 0
	for {
		idx := strings.Index(source[offset:], oldString)
		if idx < 0 {
			return true
		}
		absStart := offset + idx
		absEnd := absStart + len(oldString)
		startLine := 1 + strings.Count(source[:absStart], "\n")
		endLine := 1 + strings.Count(source[:absEnd], "\n")
		if strings.HasSuffix(oldString, "\n") && endLine > startLine {
			endLine--
		}
		if !editor.CoversLines(startLine, endLine) {
			return false
		}
		if !replaceAll {
			return true
		}
		offset = absEnd
	}
}

func runEdit(inv *invocation) error {
	if operands := inv.operands(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	path, ok := inv.value("--path")
	if !ok || path == "" {
		return inv.usage(inv.name(), "--path PATH")
	}
	oldString, ok := inv.secretSink("--old-string")
	if !ok || oldString == "" {
		return inv.usage(inv.name(), "--old-string TEXT")
	}
	newString, ok := inv.secretSink("--new-string")
	if !ok {
		return inv.usage(inv.name(), "--new-string TEXT")
	}
	if oldString == newString {
		return native.EditArgsConflict("old_string and new_string must differ")
	}
	bound, err := inv.bindStructured(path)
	if err != nil {
		return err
	}
	replaceAll := inv.flag("--replace-all")
	result, err := inv.owners.Operations.Write.ApplyStructuredEdit(
		inv.ctx,
		native.StructuredEditInput{
			Operation: inv.owners.ops[inv.name()],
			Path:      bound.Resolved.Abs,
			Memory:    inv.owners.Revisions.Memory,
		},
		inv.tc,
		func(resolved projectpaths.Resolved, source string, editor *tools.EditorDocumentText) (string, bool, error) {
			occurrences := strings.Count(source, oldString)
			if occurrences == 0 {
				return "", false, native.EditMiss(inv.ctx, inv.tc, resolved.DisplayPath, resolved.Abs, source, oldString, newString)
			}
			if occurrences > 1 && !replaceAll {
				return "", false, &toolrejection.ToolReject{Code: "EDIT_OLD_STRING_AMBIGUOUS", Data: map[string]any{"path": resolved.DisplayPath, "occurrences": occurrences}}
			}
			if !editCoversMatches(source, oldString, replaceAll, editor) {
				return "", false, editReadRequired(resolved.DisplayPath, editor.ID)
			}
			edited := strings.Replace(source, oldString, newString, 1)
			if replaceAll {
				edited = strings.ReplaceAll(source, oldString, newString)
			}
			return edited, edited != source, nil
		},
	)
	if err != nil {
		return err
	}
	if result.Accepted || result.Saved {
		inv.effects++
	}
	return nil
}

func runReplaceLines(inv *invocation) error {
	if operands := inv.operands(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	path, ok := inv.value("--path")
	if !ok || path == "" {
		return inv.usage(inv.name(), "--path PATH")
	}
	startRaw, ok := inv.value("--start")
	if !ok {
		return inv.usage(inv.name(), "--start LINE")
	}
	endRaw, ok := inv.value("--end")
	if !ok {
		return inv.usage(inv.name(), "--end LINE")
	}
	content, ok := inv.secretSink("--content")
	if !ok {
		return inv.usage(inv.name(), "--content TEXT")
	}
	startLine, err := strconv.Atoi(startRaw)
	if err != nil {
		return inv.usage(startRaw, "1-based start line")
	}
	endLine, err := strconv.Atoi(endRaw)
	if err != nil {
		return inv.usage(endRaw, "1-based end line")
	}
	bound, err := inv.bindStructured(path)
	if err != nil {
		return err
	}
	result, err := inv.owners.Operations.Write.ApplyStructuredEdit(
		inv.ctx,
		native.StructuredEditInput{
			Operation: inv.owners.ops[inv.name()],
			Path:      bound.Resolved.Abs,
			Memory:    inv.owners.Revisions.Memory,
		},
		inv.tc,
		func(resolved projectpaths.Resolved, source string, editor *tools.EditorDocumentText) (string, bool, error) {
			if editor != nil && !editor.CoversLines(startLine, endLine) {
				return "", false, editReadRequired(resolved.DisplayPath, editor.ID)
			}
			edited, err := native.ReplaceLineRange(resolved.DisplayPath, source, startLine, endLine, content)
			if err != nil {
				return "", false, err
			}
			return edited, edited != source, nil
		},
	)
	if err != nil {
		return err
	}
	if result.Accepted || result.Saved {
		inv.effects++
	}
	return nil
}

func runCodeRewrite(inv *invocation) error {
	if operands := inv.operands(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	pattern, ok := inv.value("--pattern")
	if !ok || strings.TrimSpace(pattern) == "" {
		return inv.usage(inv.name(), "--pattern PATTERN")
	}
	rewrite, ok := inv.value("--rewrite")
	if !ok || strings.TrimSpace(rewrite) == "" {
		return inv.usage(inv.name(), "--rewrite TEXT")
	}
	var paths []string
	for _, option := range inv.argv.Options("--path") {
		for _, value := range option.Values {
			if strings.TrimSpace(value.Value) != "" {
				paths = append(paths, value.Value)
			}
		}
	}
	if len(paths) == 0 {
		return inv.usage(inv.name(), "--path PATH")
	}
	lang, _ := inv.value("--lang")
	dryRun := inv.flag("--dry-run")
	recursive := inv.flag("--recursive")
	ctx := inv.ctx
	var targets []string
	for _, path := range paths {
		var bound projectpaths.BoundPath
		var err error
		if dryRun {
			bound, err = inv.bind(path, false)
		} else {
			bound, err = inv.bindStructured(path)
		}
		if err != nil {
			return err
		}
		if info, statErr := os.Stat(bound.Resolved.Abs); statErr != nil || !info.IsDir() {
			targets = append(targets, bound.Resolved.Abs)
			continue
		}
		if !recursive {
			return inv.usage(path, "a file path, or pass --recursive for a directory")
		}
		if walkErr := sandbox.SurveyWalk(inv.ctx, bound.Resolved.Abs, sandbox.SurveyOptions{}, func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if entry.DirEntry.Type().IsRegular() && (lang != "" || native.SupportsRewriteLanguage(entry.Abs)) {
				targets = append(targets, entry.Abs)
			}
			return sandbox.SurveyContinue, nil
		}); walkErr != nil {
			return walkErr
		}
	}
	for _, abs := range targets {
		if dryRun {
			source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, abs)
			if err != nil {
				return err
			}
			if err := inv.recordPin(source.Revision); err != nil {
				return err
			}
			rewritten, changed, err := native.RewriteSource(ctx, source.Path, lang, pattern, rewrite, source.Content)
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			if err := inv.previewRewrite(source.Path, source.Content, rewritten); err != nil {
				return err
			}
			continue
		}
		result, err := inv.owners.Operations.Write.ApplyStructuredEdit(
			ctx,
			native.StructuredEditInput{
				Operation: inv.owners.ops[inv.name()],
				Path:      abs,
				Memory:    inv.owners.Revisions.Memory,
			},
			inv.tc,
			func(resolved projectpaths.Resolved, source string, _ *tools.EditorDocumentText) (string, bool, error) {
				return native.RewriteSource(ctx, resolved.DisplayPath, lang, pattern, rewrite, source)
			},
		)
		if err != nil {
			return err
		}
		if result.Accepted || result.Saved {
			inv.effects++
		}
	}
	return nil
}
