package toolexecution

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

var compiledAlternativeSpecs = sync.OnceValues(func() (map[string]toolcontract.CommandSpecification, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	index := make(map[string]toolcontract.CommandSpecification, len(catalog.Commands))
	for _, spec := range catalog.Commands {
		index[spec.Name] = spec
	}
	return index, nil
})

var nextActionPurposes = map[string]string{
	"list_delete_targets": "list the paths selected for deletion and report the affected files without deleting them",
	"preview_stream":      "preview the transformed output on stdout without modifying the file in place",
	"preview_query":       "evaluate the query and inspect the output on stdout without modifying the file",
	"dry_run_preview":     "preview the matched changes in dry-run mode without modifying files",
	"check_patch":         "check whether the patch applies cleanly without modifying the worktree",
	"inspect_target":      "inspect the current contents of the target file so you can report the exact change needed",
}

// enrichNativeEffectRejection attaches mode attribution and a validated
// operation-specific read-only alternative to native child-effect refusals.
func enrichNativeEffectRejection(ctx context.Context, surface ProgramSurface, effect NativeEffect, tc tools.ToolContext, err error) {
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || (rejected.Code != "COMMAND_EFFECT_NOT_ALLOWED" && rejected.Code != "COMMAND_OPERATION_NOT_ALLOWED") {
		return
	}
	if rejected.Data == nil {
		rejected.Data = map[string]any{}
	}
	if tc.Source.AskMode {
		if _, ok := rejected.Data["session_mode"]; !ok {
			rejected.Data["session_mode"] = string(api.SessionModeAsk)
		}
		chatMode, chatOK := hostctx.RoundMode(ctx, tc.ChatSessionID())
		sessMode, sessOK := hostctx.RoundMode(ctx, tc.Identity.SessionID)
		if (chatOK && chatMode == api.SessionModeAgent) || (sessOK && sessMode == api.SessionModeAgent) {
			rejected.Data["session_mode"] = string(api.SessionModeAgent)
			rejected.Data["mode_denied"] = true
		}
	}
	if tc.Identity.ParentSessionID != "" {
		rejected.Data["worker_leg"] = true
	}

	reach := tools.CallReach(ctx)
	hasReach := reach.Known() || len(tc.Turn.TurnOfferedToolNames) > 0 || tc.Turn.TurnToolPlan.Compiled() || tc.Turn.TurnPrograms != nil
	if !reach.Known() && hasReach {
		reach = tools.TurnReach(tc)
	}
	if hasReach {
		switch reach.Status(toolreach.ProgramTool) {
		case toolreach.Absent:
			return
		case toolreach.Loadable:
			rejected.Data["tool_loadable"] = true
			rejected.Data["can_request_tools"] = true
		case toolreach.Callable:
			rejected.Data["can_command"] = true
		}
	} else {
		rejected.Data["can_command"] = true
	}

	codeInstead, nextAction := buildValidatedReadOnlyAlternative(ctx, surface, effect, tc, reach)
	if codeInstead != "" && nextAction != "" {
		rejected.Data["code_instead"] = codeInstead
		if purpose := nextActionPurposes[nextAction]; purpose != "" {
			rejected.Data["next_action"] = purpose
		} else {
			rejected.Data["next_action"] = nextAction
		}
	}
}

func buildValidatedReadOnlyAlternative(ctx context.Context, surface ProgramSurface, effect NativeEffect, tc tools.ToolContext, reach toolreach.Request) (string, string) {
	bound := effect.Operation
	if bound.Publication() != toolcontract.NoPublication {
		return "", ""
	}
	for _, word := range effect.Stage.Argv {
		if secretmatch.ContainsReferenceToken(word) || secretmatch.ContainsMalformedReferenceToken(word) {
			return "", ""
		}
	}
	if !nativeTargetsReadable(ctx, bound.Invocation(), effect, tc) {
		return "", ""
	}
	candidateCmd, candidateMode, candidateArgv, nextAction := candidateReadOnlyForm(bound, effect)
	if candidateCmd == "" || candidateMode == "" || len(candidateArgv) == 0 || nextAction == "" {
		return "", ""
	}
	for _, word := range candidateArgv {
		if word == "" || strings.ContainsAny(word, "\n\r\x00") || secretmatch.ContainsReferenceToken(word) || secretmatch.ContainsMalformedReferenceToken(word) {
			return "", ""
		}
	}
	if !validateCandidateReadOnlyForm(surface, bound, candidateCmd, candidateMode, candidateArgv, tc, reach) {
		return "", ""
	}
	return toolreach.QuoteProgram(candidateArgv), nextAction
}

func nativeTargetsReadable(ctx context.Context, inv toolcontract.OperationInvocation, effect NativeEffect, tc tools.ToolContext) bool {
	if tc.CheckScopePolicy(ctx, inv.Scope.RootID, inv.Scope.Directory, false) != nil {
		return false
	}
	if effect.Argv == nil {
		return true
	}
	for _, target := range effect.Argv.PathTargets {
		if target.Path == "" {
			return false
		}
		if tc.CheckScopePolicy(ctx, inv.Scope.RootID, target.Path, false) != nil {
			return false
		}
		if len(tc.Source.Roots) > 0 && !pathWithinAllowedReadRoots(target.Path, tc) {
			return false
		}
	}
	return true
}

func pathWithinAllowedReadRoots(path string, tc tools.ToolContext) bool {
	if !filepath.IsAbs(path) {
		return true
	}
	for _, root := range tc.Source.Roots {
		if root.Path != "" && withinDir(root.Path, path) {
			return true
		}
	}
	for _, allowed := range []string{tc.Host.SessionScratchDir, tc.Host.SessionOutputDir, tc.Host.HostDataDir, tc.Source.WorkerBranchRoot} {
		if allowed != "" && withinDir(allowed, path) {
			return true
		}
	}
	for _, root := range tc.Files.ReadRoots {
		if root != "" && withinDir(root, path) {
			return true
		}
	}
	return false
}

func withinDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func candidateReadOnlyForm(bound toolcontract.BoundOperation, effect NativeEffect) (command, mode string, argv []string, nextAction string) {
	cmd := bound.Command()
	boundMode := bound.Mode()
	stageArgv := effect.Stage.Argv
	switch cmd {
	case "find":
		if boundMode != "-delete" || len(stageArgv) < 2 {
			return "", "", nil, ""
		}
		filtered := make([]string, 0, len(stageArgv))
		for _, w := range stageArgv {
			switch w {
			case "-delete":
				continue
			case "-exec", "-execdir", "-ok", "-okdir":
				return "", "", nil, ""
			default:
				filtered = append(filtered, w)
			}
		}
		if len(filtered) == len(stageArgv) {
			return "", "", nil, ""
		}
		return "find", "default-predicates-print", filtered, "list_delete_targets"

	case "sed":
		if boundMode != "-i" || effect.Argv == nil || len(effect.Argv.PathTargets) == 0 {
			return "", "", nil, ""
		}
		stripped, ok := stripInPlaceFlag("sed", stageArgv)
		if !ok {
			return "", "", nil, ""
		}
		return "sed", "stream-script", stripped, "preview_stream"

	case "yq":
		if boundMode != "-i" || effect.Argv == nil || len(effect.Argv.PathTargets) == 0 {
			return "", "", nil, ""
		}
		stripped, ok := stripInPlaceFlag("yq", stageArgv)
		if !ok {
			return "", "", nil, ""
		}
		return "yq", "query", stripped, "preview_query"

	case "code-rewrite":
		if boundMode != "mutation" || len(stageArgv) < 2 || slices.Contains(stageArgv, "--dry-run") {
			return "", "", nil, ""
		}
		return "code-rewrite", "dry-run", append(slices.Clone(stageArgv), "--dry-run"), "dry_run_preview"

	case "patch":
		if boundMode != "mutation" || len(stageArgv) < 2 || slices.Contains(stageArgv, "--dry-run") {
			return "", "", nil, ""
		}
		return "patch", "dry-run", append(slices.Clone(stageArgv), "--dry-run"), "dry_run_preview"

	case "git rm", "git mv":
		if boundMode != "mutation" || len(stageArgv) < 3 || slices.Contains(stageArgv, "-n") || slices.Contains(stageArgv, "--dry-run") {
			return "", "", nil, ""
		}
		return cmd, "dry-run", insertAfterCommandPrefix(cmd, stageArgv, "--dry-run"), "dry_run_preview"

	case "git apply":
		if boundMode != "mutation" || len(stageArgv) < 3 || slices.Contains(stageArgv, "--check") {
			return "", "", nil, ""
		}
		for _, disallowed := range []string{"--index", "--cached", "-3", "--3way"} {
			if slices.Contains(stageArgv, disallowed) {
				return "", "", nil, ""
			}
		}
		return "git apply", "read-only", insertAfterCommandPrefix("git apply", stageArgv, "--check"), "check_patch"

	case "rm", "rmdir":
		if effect.Argv == nil || len(effect.Argv.Operands) == 0 {
			return "", "", nil, ""
		}
		args := []string{"ls", "-ld"}
		needDash := false
		for _, op := range effect.Argv.Operands {
			if strings.HasPrefix(op, "-") {
				needDash = true
				break
			}
		}
		if needDash {
			args = append(args, "--")
		}
		args = append(args, effect.Argv.Operands...)
		return "ls", "list", args, "list_delete_targets"

	case "edit", "replace-lines", "jq-edit":
		path := extractSingleTargetPath(effect)
		if path == "" {
			return "", "", nil, ""
		}
		args := []string{"cat", "-n"}
		if strings.HasPrefix(path, "-") {
			args = append(args, "--")
		}
		args = append(args, path)
		return "cat", "read", args, "inspect_target"
	}
	return "", "", nil, ""
}

func stripInPlaceFlag(command string, argv []string) ([]string, bool) {
	specs, err := compiledAlternativeSpecs()
	if err != nil {
		return nil, false
	}
	spec, ok := specs[command]
	if !ok || len(argv) < 2 {
		return nil, false
	}
	out := make([]string, 0, len(argv))
	out = append(out, argv[0])
	removed := false
	endOfOptions := false
	for i := 1; i < len(argv); i++ {
		w := argv[i]
		if endOfOptions {
			out = append(out, w)
			continue
		}
		if w == "--" {
			endOfOptions = true
			out = append(out, w)
			continue
		}
		if command == "sed" {
			if w == "-i" || w == "-I" || w == "--in-place" {
				removed = true
				if i+1 < len(argv) && argv[i+1] == "" {
					i++
				}
				continue
			}
			if strings.HasPrefix(w, "-i") || strings.HasPrefix(w, "-I") || strings.HasPrefix(w, "--in-place=") {
				removed = true
				continue
			}
		}
		if command == "yq" {
			if w == "-i" || w == "--inplace" {
				removed = true
				continue
			}
		}
		out = append(out, w)
	}
	if !removed {
		return nil, false
	}
	fields := make(commandprogram.Fields, len(out))
	for i, w := range out {
		fields[i] = commandprogram.Field{Value: w}
	}
	bound, err := (commandprogram.CatalogBinder{}).Bind(spec, fields)
	if err != nil {
		return nil, false
	}
	if _, stillInPlace := bound.Last("-i"); stillInPlace {
		return nil, false
	}
	if _, stillInPlace := bound.Last("-I"); stillInPlace {
		return nil, false
	}
	return out, true
}

func insertAfterCommandPrefix(command string, argv []string, flag string) []string {
	parts := strings.Split(command, " ")
	idx := 1
	if parts[0] == "git" && len(parts) > 1 {
		for idx < len(argv) {
			word := argv[idx]
			switch {
			case word == "-C" || word == "-c":
				idx += 2
				continue
			case strings.HasPrefix(word, "-C") || strings.HasPrefix(word, "-c") || word == "--no-pager":
				idx++
				continue
			}
			break
		}
		idx += len(parts) - 1
	}
	if idx > len(argv) {
		idx = len(argv)
	}
	out := make([]string, 0, len(argv)+1)
	out = append(out, argv[:idx]...)
	out = append(out, flag)
	out = append(out, argv[idx:]...)
	return out
}

func extractSingleTargetPath(effect NativeEffect) string {
	if effect.Argv != nil {
		for _, opt := range effect.Argv.Options {
			if opt.Option == "--path" && len(opt.Values) == 1 && opt.Values[0] != "" {
				return opt.Values[0]
			}
		}
	}
	if path, ok := effect.Arguments["path"].(string); ok && path != "" {
		return path
	}
	return ""
}

func validateCandidateReadOnlyForm(surface ProgramSurface, bound toolcontract.BoundOperation, candidateCmd, candidateMode string, candidateArgv []string, tc tools.ToolContext, reach toolreach.Request) bool {
	specs, err := compiledAlternativeSpecs()
	if err != nil {
		return false
	}
	spec, ok := specs[candidateCmd]
	if !ok {
		return false
	}
	fields := make(commandprogram.Fields, len(candidateArgv))
	for i, w := range candidateArgv {
		fields[i] = commandprogram.Field{Value: w}
	}
	if _, err := (commandprogram.CatalogBinder{}).Bind(spec, fields); err != nil {
		return false
	}
	if reach.Known() && tc.Turn.TurnPrograms != nil && !reach.Program(candidateCmd) {
		return false
	}
	candidateDef, ok := findSurfaceOperationDefinition(surface, bound.Definition(), candidateCmd)
	if !ok {
		return false
	}
	var childEffects *toolcontract.CommandEffects
	for _, eff := range spec.Effects {
		if eff.Mode == candidateMode && slices.Contains(eff.Requires, "bound-child-effect-set") {
			ro, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
			childEffects = &ro
			break
		}
	}
	candidateBound, err := toolcontract.BindOperation(candidateDef, bound.Invocation(), candidateCmd, candidateMode, childEffects)
	if err != nil {
		return false
	}
	return surface.ValidateOperation(candidateBound) == nil
}

func findSurfaceOperationDefinition(surface ProgramSurface, current toolcontract.OperationDefinition, command string) (toolcontract.OperationDefinition, bool) {
	for _, cmd := range current.ReadOnlyFacts().Commands {
		if cmd.Name == command {
			return current, true
		}
	}
	for _, def := range surface.selection.Definitions() {
		for _, cmd := range def.ReadOnlyFacts().Commands {
			if cmd.Name == command {
				return def, true
			}
		}
	}
	return toolcontract.OperationDefinition{}, false
}
