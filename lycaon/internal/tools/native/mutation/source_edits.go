package mutation

import (
	"os"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
)

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
	bound, err := inv.bind(path, true)
	if err != nil {
		return err
	}
	source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(source.Revision); err != nil {
		return err
	}
	destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(destination); err != nil {
		return err
	}
	occurrences := strings.Count(source.Content, oldString)
	if occurrences == 0 {
		return native.EditMiss(inv.ctx, inv.tc, bound.Resolved.DisplayPath, bound.Resolved.Abs, source.Content, oldString, newString)
	}
	replaceAll := inv.flag("--replace-all")
	if occurrences > 1 && !replaceAll {
		return &toolrejection.ToolReject{Code: "EDIT_OLD_STRING_AMBIGUOUS", Data: map[string]any{"path": bound.Resolved.DisplayPath, "occurrences": occurrences}}
	}
	edited := strings.Replace(source.Content, oldString, newString, 1)
	if replaceAll {
		edited = strings.ReplaceAll(source.Content, oldString, newString)
	}
	result, err := inv.owners.Operations.Write.Apply(inv.ctx, native.WriteInput{Operation: inv.owners.ops[inv.name()], Path: bound.Resolved.Abs, Content: edited, Memory: inv.owners.Revisions.Memory,
		Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, inv.tc)
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
	bound, err := inv.bind(path, true)
	if err != nil {
		return err
	}
	source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(source.Revision); err != nil {
		return err
	}
	destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(destination); err != nil {
		return err
	}
	edited, err := native.ReplaceLineRange(bound.Resolved.DisplayPath, source.Content, startLine, endLine, content)
	if err != nil {
		return err
	}
	if edited == source.Content {
		return nil
	}
	result, err := inv.owners.Operations.Write.Apply(inv.ctx, native.WriteInput{Operation: inv.owners.ops[inv.name()], Path: bound.Resolved.Abs, Content: edited, Memory: inv.owners.Revisions.Memory,
		Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, inv.tc)
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
		bound, err := inv.bind(path, !dryRun)
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
		if dryRun {
			if err := inv.previewRewrite(source.Path, source.Content, rewritten); err != nil {
				return err
			}
			continue
		}
		destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, abs)
		if err != nil {
			return err
		}
		if err := inv.recordPin(destination); err != nil {
			return err
		}
		result, err := inv.owners.Operations.Write.Apply(ctx, native.WriteInput{Operation: inv.owners.ops[inv.name()], Path: abs, Content: rewritten, Memory: inv.owners.Revisions.Memory,
			Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, inv.tc)
		if err != nil {
			return err
		}
		if result.Accepted || result.Saved {
			inv.effects++
		}
	}
	return nil
}
