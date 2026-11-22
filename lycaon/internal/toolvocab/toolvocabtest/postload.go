package toolvocabtest

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandmode"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
)

func isBacktickAuthorizedByLoad(
	cat *toolvocab.Catalog,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	auth ActorAuthority,
	sessionMode api.SessionMode,
	offered map[string]bool,
	postHeld map[string]bool,
	postReach toolreach.Request,
	sentence string,
	bs BacktickSpan,
	bindings []loadBinding,
) (authorized bool, schemaViolations []AttributedClaimIssue) {
	raw := strings.TrimSpace(bs.Span)
	for _, b := range bindings {
		if !isSpanSequencedWithLoader(sentence, bs.Start, bs.End, b) {
			continue
		}
		if raw == b.target {
			return true, nil
		}
		if rest, hasDot := strings.CutPrefix(raw, b.target+"."); hasDot {
			return validatePostLoadDottedSpan(b.target, rest, raw, bs.Line, auth, idx, postHeld, sessionMode)
		}
		if strings.HasPrefix(raw, b.target+"(") {
			return validatePostLoadCallSpan(cat, b.target, raw, bs.Line, auth, idx, vocab, postHeld, postReach, sessionMode)
		}
		if b.target == toolreach.ProgramTool && vocab != nil {
			if ok, iss, matched := validatePostLoadCommandSpan(raw, bs.Line, vocab, postHeld, postReach); matched {
				return ok, iss
			}
		}
		if ok, iss, matched := validatePostLoadBareFieldSpan(cat, b.target, raw, bs.Line, auth, idx, offered, postHeld, sessionMode); matched {
			return ok, iss
		}
	}
	return false, nil
}

func validatePostLoadDottedSpan(
	target, rest, raw string,
	line int,
	auth ActorAuthority,
	idx *PrunableSchemaIndex,
	postHeld map[string]bool,
	sessionMode api.SessionMode,
) (bool, []AttributedClaimIssue) {
	p, val, hasVal, ell := splitPathAndOptionalValue(rest)
	norm := normalizeSchemaPath(p)
	projSchema, hasSchema := resolvePostLoadSchema(target, auth, idx, postHeld, sessionMode)
	if !hasSchema {
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: norm, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load schema for %q is uncertified; cannot verify %q", target, raw),
		}}
	}
	if !toolschema.SchemaAcceptsField(projSchema, norm) && !schemaAcceptsPath(projSchema, norm) {
		rep := missingPathHead(projSchema, norm)
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: rep, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load %s projected schema lacks %q", target, rep),
		}}
	}
	if hasVal && !ell {
		if iss := validateValueAtPath(target, projSchema, norm, val, line); len(iss) > 0 {
			return false, iss
		}
	}
	return true, nil
}

func validatePostLoadCallSpan(
	cat *toolvocab.Catalog,
	target, raw string,
	line int,
	auth ActorAuthority,
	idx *PrunableSchemaIndex,
	vocab *BacktickVocab,
	postHeld map[string]bool,
	postReach toolreach.Request,
	sessionMode api.SessionMode,
) (bool, []AttributedClaimIssue) {
	calls := ParseCallExamples(cat, raw)
	if len(calls) == 0 {
		return false, nil
	}
	projSchema, hasSchema := resolvePostLoadSchema(target, auth, idx, postHeld, sessionMode)
	for _, ex := range calls {
		if ex.Tool != target {
			continue
		}
		if !hasSchema {
			if len(ex.Args) == 0 && ex.HasEllipsis {
				continue
			}
			return false, []AttributedClaimIssue{{
				Tool: target, AttributedTools: []string{target}, Path: target, Span: raw, Line: line,
				Reason: fmt.Sprintf("post-load schema for %q is uncertified; cannot verify call %q", target, raw),
			}}
		}
		if iss := validateCallAgainstSchema(ex, projSchema); len(iss) > 0 {
			return false, iss
		}
		if target == toolreach.ProgramTool {
			if cmdVal, hasCmd := ex.Args["command"]; hasCmd {
				if cmdStr, isConcrete := concreteStringOrIdent(cmdVal); isConcrete && !isPlaceholderText(cmdStr) {
					if ok, reason := checkAllCommandSegments(vocab, postReach, postHeld, cmdStr); !ok {
						return false, []AttributedClaimIssue{{
							Tool: target, AttributedTools: []string{target}, Path: "command", Value: cmdStr, Span: raw, Line: line,
							Reason: reason,
						}}
					}
				}
			}
		}
	}
	return true, nil
}

func validatePostLoadCommandSpan(
	raw string,
	line int,
	vocab *BacktickVocab,
	postHeld map[string]bool,
	postReach toolreach.Request,
) (bool, []AttributedClaimIssue, bool) {
	first := firstCommandWord(raw)
	if first == "" || !vocab.commands[first] {
		return false, nil, false
	}
	if ok, reason := checkAllCommandSegments(vocab, postReach, postHeld, raw); !ok {
		return false, []AttributedClaimIssue{{
			Tool: toolreach.ProgramTool, AttributedTools: []string{toolreach.ProgramTool}, Path: raw, Span: raw, Line: line,
			Reason: reason,
		}}, true
	}
	return true, nil, true
}

func validatePostLoadBareFieldSpan(
	cat *toolvocab.Catalog,
	target, raw string,
	line int,
	auth ActorAuthority,
	idx *PrunableSchemaIndex,
	offered map[string]bool,
	postHeld map[string]bool,
	sessionMode api.SessionMode,
) (bool, []AttributedClaimIssue, bool) {
	if idx == nil || strings.Contains(raw, "(") || (strings.Contains(raw, " ") && !strings.ContainsAny(raw, ":=")) {
		return false, nil, false
	}
	p, val, hasVal, ell := splitPathAndOptionalValue(raw)
	norm := normalizeSchemaPath(p)
	if norm == "" || (cat != nil && cat.KnownName(norm) && norm != target) {
		return false, nil, false
	}
	owners := idx.AllOwners(norm)
	if !slices.Contains(owners, target) || anyOfferedOwner(owners, offered) {
		return false, nil, false
	}
	projSchema, hasSchema := resolvePostLoadSchema(target, auth, idx, postHeld, sessionMode)
	if !hasSchema {
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: norm, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load schema for %q is uncertified; cannot verify %q", target, raw),
		}}, true
	}
	if !toolschema.SchemaAcceptsField(projSchema, norm) && !schemaAcceptsPath(projSchema, norm) {
		rep := missingPathHead(projSchema, norm)
		return false, []AttributedClaimIssue{{
			Tool: target, AttributedTools: []string{target}, Path: rep, Span: raw, Line: line,
			Reason: fmt.Sprintf("post-load %s projected schema lacks %q", target, rep),
		}}, true
	}
	if hasVal && !ell {
		if iss := validateValueAtPath(target, projSchema, norm, val, line); len(iss) > 0 {
			return false, iss, true
		}
	}
	return true, nil, true
}

func anyOfferedOwner(owners []string, offered map[string]bool) bool {
	for _, o := range owners {
		if offered[o] {
			return true
		}
	}
	return false
}

func resolvePostLoadSchema(
	tool string,
	auth ActorAuthority,
	idx *PrunableSchemaIndex,
	postHeld map[string]bool,
	sessionMode api.SessionMode,
) (map[string]any, bool) {
	if auth.PostLoadSchemas != nil {
		s, ok := auth.PostLoadSchemas[tool]
		return s, ok && s != nil
	}
	if idx == nil {
		return nil, false
	}
	base, ok := idx.BaseSchema(tool)
	if !ok || base == nil {
		return nil, false
	}
	opts := toolschema.ProjectOptions{
		CommandReadOnly:     !postHeld[toolcontract.ReachCommandExternalExec],
		AskMode:             sessionMode == api.SessionModeAsk || readerIsAskMode(auth.Reader),
		PruneWaitConditions: true,
	}
	if auth.WaitConditionsKnown {
		opts.WaitConditions = auth.WaitConditions
	}
	return toolschema.ProjectToolArgsSchema(tool, base, opts), true
}

func checkCommandInvocations(
	vocab *BacktickVocab,
	auth ActorAuthority,
	held map[string]bool,
	label string,
	subj ClaimSubject,
	sentence string,
	line int,
) []SubjectViolation {
	if vocab == nil {
		return nil
	}
	curReach := currentCommandReach(auth, held)
	var out []SubjectViolation
	for _, bs := range ExtractInlineBackticks(sentence) {
		raw := strings.TrimSpace(bs.Span)
		if raw == "" || strings.Contains(raw, "(") || !strings.Contains(raw, " ") {
			continue
		}
		for _, seg := range chainSplitRe.Split(raw, -1) {
			first := firstCommandWord(seg)
			if first == "" || !vocab.commands[first] {
				continue
			}
			if ok, reason := checkSingleCommandSegment(curReach, held, seg); !ok {
				out = append(out, SubjectViolation{
					Label: label, Subject: subj, Claim: raw, Line: line,
					Reason: reason,
				})
			}
		}
	}
	return out
}

func currentCommandReach(auth ActorAuthority, held map[string]bool) toolreach.Request {
	if !held[toolreach.ProgramTool] {
		return toolreach.Offered(nil)
	}
	table := auth.ProgramTable
	if table == nil {
		progReach := auth.ProgramReach
		if progReach == nil {
			progReach = func(name string) bool { return toolcontract.CommandLanguageReachable(name, held) }
		}
		table = deriveProgramTable(auth.Reader.Effects(), auth.Reader.SurfaceID(), readerExternalHidden(auth.Reader), progReach)
	}
	return toolreach.Offered([]string{toolreach.ProgramTool}).WithProgramTable(table)
}

func firstCommandWord(s string) string {
	words := splitCommandWords(strings.TrimSpace(s))
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

func checkAllCommandSegments(vocab *BacktickVocab, reach toolreach.Request, held map[string]bool, cmd string) (bool, string) {
	for _, seg := range chainSplitRe.Split(cmd, -1) {
		first := firstCommandWord(seg)
		if first == "" || (vocab != nil && !vocab.commands[first]) {
			continue
		}
		if ok, reason := checkSingleCommandSegment(reach, held, seg); !ok {
			return false, reason
		}
	}
	return true, ""
}

func checkSingleCommandSegment(reach toolreach.Request, held map[string]bool, seg string) (bool, string) {
	if !held[toolreach.ProgramTool] {
		return false, fmt.Sprintf("command invocation %q requires offered command tool", seg)
	}
	if hasUnquotedOutputRedirect(seg) && !held[toolcontract.ReachCommandMutation] {
		return false, fmt.Sprintf("output redirect in %q requires command mutation", seg)
	}
	words := normalizeProseCommandWords(splitCommandWords(strings.TrimSpace(seg)))
	if len(words) == 0 {
		return true, ""
	}
	if len(words) == 1 {
		if !reach.Program(words[0]) {
			return false, fmt.Sprintf("command %q is not admitted on target program surface", words[0])
		}
		return true, ""
	}
	if reach.ProgramInvocation(words...) {
		return true, ""
	}
	if len(words) == 2 && strings.HasPrefix(words[1], "-") && reach.ProgramInvocation(words[0], words[1], "PATTERN") {
		return true, ""
	}
	if bound, ok := commandmode.Resolve(context.Background(), words); ok {
		return false, fmt.Sprintf("command mode %s[%s] in %q is not admitted", bound.Command, bound.Mode, seg)
	}
	return false, fmt.Sprintf("command invocation %q does not match an admitted command form", seg)
}

func deriveProgramTable(allowed toolcontract.CommandEffects, surfaceID string, externalHidden bool, programAllowed func(string) bool) *toolreach.ProgramTable {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil
	}
	surfaceTools := surfaceFormTools(surfaceID)
	reachable := make(map[string]bool, len(catalog.Commands))
	modeReach := make(map[string]map[string]bool, len(catalog.Commands))
	for _, spec := range catalog.Commands {
		permitted := programAllowed == nil || programAllowed(spec.Name)
		if surfaceTools != nil && !surfaceAllowsSpecForm(surfaceTools, spec.Name) {
			permitted = false
		}
		if externalHidden && spec.Name == "external" {
			permitted = false
		}
		admittedModes := make(map[string]bool, len(spec.Effects))
		anyAllowed := false
		for _, req := range spec.Effects {
			eff, effErr := req.CommandEffects(&allowed)
			modeOK := permitted && effErr == nil && allowed.Allows(eff)
			admittedModes[req.Mode] = modeOK
			anyAllowed = anyAllowed || modeOK
		}
		modeReach[spec.Name] = admittedModes
		reachable[spec.Name] = anyAllowed
		for _, al := range spec.Aliases {
			if anyAllowed {
				reachable[al] = true
			}
		}
	}
	return toolreach.NewProgramTable(reachable, modeReach)
}

func surfaceFormTools(surfaceID string) map[string]bool {
	if strings.TrimSpace(surfaceID) == "" {
		return nil
	}
	cat, err := surfacecatalog.Load()
	if err != nil {
		return nil
	}
	row, err := cat.Surface(surfaceID)
	if err != nil {
		return nil
	}
	toolsMap := make(map[string]bool, len(row.Floor)+len(row.Loadable))
	for _, name := range append(row.FloorTools(), row.LoadableTools()...) {
		toolsMap[name] = true
	}
	return toolsMap
}

func surfaceAllowsSpecForm(surfaceTools map[string]bool, command string) bool {
	ids, err := toolcontract.CommandPolicyIdentities(command)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if toolcontract.CommandFormTool(id) && !surfaceTools[id] {
			return false
		}
	}
	return true
}

func readerExternalHidden(r prompts.Reader) bool {
	if vars, err := prompts.Vars(r, map[string]any{}); err == nil {
		hidden, _ := vars["command_external_hidden"].(bool)
		return hidden
	}
	return false
}

func readerIsAskMode(r prompts.Reader) bool {
	if vars, err := prompts.Vars(r, map[string]any{}); err == nil {
		mode, _ := vars["session_mode"].(string)
		return mode == string(api.SessionModeAsk)
	}
	return false
}

func buildPostLoadHeld(r prompts.Reader) map[string]bool {
	toolSet := map[string]bool{}
	var visible []string
	for _, name := range append(slices.Clone(r.Offered()), r.Requestable()...) {
		if !toolSet[name] {
			toolSet[name] = true
			visible = append(visible, name)
		}
	}
	sort.Strings(visible)
	hidden := readerExternalHidden(r)
	const pid = "post_load"
	postReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: pid,
		Profiles: []sandbox.ToolProfile{{
			ID:             pid,
			Tools:          toolSet,
			CommandEffects: r.Effects(),
		}},
		Visible:               visible,
		Loaded:                toolSet,
		RootCount:             1,
		HasGit:                true,
		WebSearch:             true,
		CommandExternalHidden: hidden,
	})
	if err != nil {
		return r.Held()
	}
	return postReader.Held()
}

func splitCommandWords(seg string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(seg) {
				i++
				cur.WriteByte(seg[i])
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			inWord = true
			continue
		}
		if ch == ' ' || ch == '\t' {
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
			continue
		}
		cur.WriteByte(ch)
		inWord = true
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

func normalizeProseCommandWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "…" || w == "..." {
			continue
		}
		switch w {
		case "A,Bp":
			w = "1,2p"
		case "A,B":
			w = "1,2"
		}
		out = append(out, w)
	}
	return out
}

func hasUnquotedOutputRedirect(seg string) bool {
	var quote byte
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if quote != 0 {
			if ch == '\\' && i+1 < len(seg) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '>' && (i == 0 || (seg[i-1] != '-' && seg[i-1] != '=')) && (i+1 >= len(seg) || seg[i+1] != '&') {
			return true
		}
	}
	return false
}
