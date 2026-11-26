package toolvocabtest

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

var chainSplitRe = regexp.MustCompile(`\s*(?:&&|\|\||[|;])\s*`)

// schemaPathSpanRe matches a dotted argument path, `a.b` or `a[].b`.
var schemaPathSpanRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\[\])?(?:\.[a-z][a-z0-9_]*(?:\[\])?)+$`)

// claimGrammar reads one actor's claims from rendered text by protocol
// grammar alone: tool mentions, call expressions with their arguments,
// tool.path spans, and command code spans, each within its block.
type claimGrammar struct {
	cat         *toolvocab.Catalog
	idx         *PrunableSchemaIndex
	vocab       *BacktickVocab
	auth        ActorAuthority
	held        map[string]bool
	requestable map[string]bool
	reachable   map[string]bool
	curReach    toolreach.Request
	postReach   toolreach.Request
	curMut      bool
	postMut     bool
}

func newClaimGrammar(cat *toolvocab.Catalog, idx *PrunableSchemaIndex, vocab *BacktickVocab, auth ActorAuthority) claimGrammar {
	g := claimGrammar{
		cat: cat, idx: idx, vocab: vocab, auth: auth,
		held:        auth.Reader.Held(),
		requestable: map[string]bool{},
		reachable:   auth.Reader.Held(),
		postReach:   toolreach.Offered(nil),
		postMut:     effectsAllowMutation(auth.Reader.Effects()),
	}
	for _, name := range auth.Reader.Requestable() {
		g.requestable[name] = true
		g.reachable[name] = true
	}
	g.curReach = currentCommandReach(auth, g.held)
	g.curMut = g.held[toolcontract.ReachCommandMutation]
	if auth.PostLoadPrograms != nil && g.requestable[toolreach.ProgramTool] {
		g.postReach = toolreach.Offered([]string{toolreach.ProgramTool}).WithProgramTable(auth.PostLoadPrograms)
	}
	return g
}

func (g claimGrammar) evaluate(block ClaimBlock) []SubjectViolation {
	if g.vocab != nil && g.vocab.IsAuthoringReferenceSource(block.Label) {
		g.vocab = nil
	}
	var out []SubjectViolation
	for _, b := range toolvocab.Blocks(block.Text) {
		out = append(out, g.block(block, b)...)
	}
	return deduplicateSubjectViolations(out)
}

func (g claimGrammar) block(block ClaimBlock, b toolvocab.Block) []SubjectViolation {
	lineAt := func(off int) int { return b.Line + strings.Count(b.Text[:max(off, 0)], "\n") }
	var out []SubjectViolation
	report := func(claim string, off int, reason string) {
		out = append(out, SubjectViolation{Label: block.Label, Subject: block.Subject, Claim: claim, Line: lineAt(off), Reason: reason})
	}
	for _, name := range toolvocab.UnreachableNames(g.cat, b.Text, g.reachable) {
		report(name, strings.Index(b.Text, strings.TrimSuffix(name, "#")), fmt.Sprintf("names unreachable tool, operation, or handle %q", name))
	}
	calls := toolvocab.Calls(g.cat, b.Text)
	for _, ex := range ParseCallExamples(g.cat, b.Text) {
		g.checkCall(ex, calls, report)
	}
	for _, bs := range ExtractInlineBackticks(b.Text) {
		g.checkToolPath(bs, report)
		g.checkSchemaPath(bs, report)
		g.checkCommandSpan(bs, calls, report)
	}
	return out
}

func (g claimGrammar) checkCall(ex CallExample, calls []toolvocab.Call, report func(string, int, string)) {
	if !g.reachable[ex.Tool] {
		report(ex.Tool, ex.Start, fmt.Sprintf("names unreachable tool, operation, or handle %q", ex.Tool))
		return
	}
	if ex.Tool == toolreach.LoaderTool {
		g.checkLoader(ex, report)
	}
	schema, ok := g.offeredSchema(ex.Tool)
	reach, mut := g.curReach, g.curMut
	if !g.held[ex.Tool] {
		if !toolvocab.LoadedBefore(g.cat, calls, ex.Tool, ex.Start) {
			report(ex.Tool, ex.Start, fmt.Sprintf("calls requestable tool %q without an earlier request_tools(need=%q) in its block", ex.Tool, ex.Tool))
			return
		}
		if schema, ok = g.postLoadSchema(ex.Tool); !ok {
			report(ex.Tool, ex.Start, fmt.Sprintf("post-load schema for %q is uncertified; cannot verify call %q", ex.Tool, ex.Raw))
			return
		}
		reach, mut = g.postReach, g.postMut
	}
	if ok {
		for _, iss := range validateCallAgainstSchema(ex, schema) {
			report(claimOf(iss), ex.Start, iss.Reason)
		}
	}
	if ex.Tool != toolreach.ProgramTool {
		return
	}
	if cmd, concrete := concreteStringOrIdent(ex.Args["command"]); concrete && !isPlaceholderText(cmd) {
		if okCmd, reason := checkAllCommandSegments(g.vocab, reach, mut, cmd); !okCmd {
			report("command", ex.Start, reason)
		}
	}
}

func (g claimGrammar) checkLoader(ex CallExample, report func(string, int, string)) {
	needRaw, hasNeed := ex.Args["need"]
	if !hasNeed {
		if !ex.HasEllipsis {
			report(ex.Raw, ex.Start, "request_tools call missing required need argument")
		}
		return
	}
	need, ok := concreteStringOrIdent(needRaw)
	if !ok || isPlaceholderText(need) {
		return
	}
	switch {
	case g.cat.KnownName(need) && !g.requestable[need]:
		report(ex.Raw, ex.Start, fmt.Sprintf("request_tools target %q is not in Requestable()", need))
	case len(g.requestable) == 0:
		report(ex.Raw, ex.Start, "request_tools call rendered with zero requestable tools")
	}
}

func (g claimGrammar) checkToolPath(bs BacktickSpan, report func(string, int, string)) {
	head, rest, ok := toolPathSpan(g.cat, bs.Span)
	if !ok {
		return
	}
	if !g.reachable[head] {
		report(head, bs.Start, fmt.Sprintf("names unreachable tool, operation, or handle %q", head))
		return
	}
	schema, ok := g.offeredSchema(head)
	if !g.held[head] {
		if schema, ok = g.postLoadSchema(head); !ok {
			report(normalizeSchemaPath(rest), bs.Start, fmt.Sprintf("post-load schema for %q is uncertified; cannot verify %q", head, bs.Span))
			return
		}
	}
	if !ok {
		return
	}
	for _, iss := range validateToolPath(head, schema, rest, bs.Span, bs.Line) {
		report(claimOf(iss), bs.Start, iss.Reason)
	}
}

// checkSchemaPath reads a dotted argument path with no tool head, such as
// `capability_request.local_listen`: a declared schema path names an argument
// some tool accepts, so a tool the reader holds, or can request with a
// certified post-load schema, must accept the whole path. A single field name
// outside a call makes no claim (decision 005 rule 3).
func (g claimGrammar) checkSchemaPath(bs BacktickSpan, report func(string, int, string)) {
	raw := strings.TrimSpace(bs.Span)
	if g.vocab == nil || !schemaPathSpanRe.MatchString(raw) {
		return
	}
	if _, _, ok := toolPathSpan(g.cat, raw); ok {
		return
	}
	p := normalizeSchemaPath(raw)
	if !g.vocab.schemaItems[p] {
		return
	}
	for tool := range g.held {
		if s, ok := g.offeredSchema(tool); ok && schemaAcceptsPath(s, p) {
			return
		}
	}
	for tool := range g.requestable {
		if s, ok := g.postLoadSchema(tool); ok && schemaAcceptsPath(s, p) {
			return
		}
	}
	report(raw, bs.Start, fmt.Sprintf("no reachable tool's projected schema accepts %q", raw))
}

// checkCommandSpan reads a command code span with the production command
// grammar: each chained segment led by a declared command must be admitted on
// the reach the reader holds at that point in the block.
func (g claimGrammar) checkCommandSpan(bs BacktickSpan, calls []toolvocab.Call, report func(string, int, string)) {
	raw := strings.TrimSpace(bs.Span)
	if g.vocab == nil || strings.Contains(raw, "(") || !strings.Contains(raw, " ") {
		return
	}
	reach, mut := g.curReach, g.curMut
	if !g.held[toolreach.ProgramTool] && g.requestable[toolreach.ProgramTool] && toolvocab.LoadedBefore(g.cat, calls, toolreach.ProgramTool, bs.Start) {
		reach, mut = g.postReach, g.postMut
	}
	for _, seg := range chainSplitRe.Split(raw, -1) {
		if first := firstCommandWord(seg); first == "" || !g.vocab.commands[first] {
			continue
		}
		if ok, reason := checkSingleCommandSegment(reach, mut, seg); !ok {
			report(raw, bs.Start, reason)
		}
	}
}

func (g claimGrammar) offeredSchema(tool string) (map[string]any, bool) {
	if g.auth.Schemas != nil {
		s, ok := g.auth.Schemas[tool]
		return s, ok
	}
	if g.idx != nil {
		return g.idx.BaseSchema(tool)
	}
	return nil, false
}

func (g claimGrammar) postLoadSchema(tool string) (map[string]any, bool) {
	s, ok := g.auth.PostLoadSchemas[tool]
	return s, ok && s != nil
}

func claimOf(iss AttributedClaimIssue) string {
	if iss.Path != "" {
		return iss.Path
	}
	return iss.Span
}
