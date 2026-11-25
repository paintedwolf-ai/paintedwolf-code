package wiring

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
)

// reachOracle reads names in request text against the stock vocabulary and
// the executor's command admission. It holds nothing a renderer produces.
type reachOracle struct {
	names    *toolvocab.Catalog
	profiles []sandbox.ToolProfile
}

func newReachOracle(t *testing.T) *reachOracle {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "discover stock content", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "load tool profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "load tool schemas", err)
	names, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "tool vocabulary", err)
	return &reachOracle{names: names, profiles: profiles}
}

// argFields is every argument path a schema accepts, with each path's head.
func argFields(schema map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, path := range toolschema.ArgFieldPaths(schema) {
		head, _, _ := strings.Cut(path, ".")
		out[path], out[head] = true, true
	}
	return out
}

// commandReach is what the executor admits through command for a profile on
// a surface, from the session's posture-narrowed profiles, as the reach
// markers the vocabulary reads.
func commandReach(t *testing.T, profiles []sandbox.ToolProfile, profileID, surfaceID string) map[string]bool {
	t.Helper()
	if profileID == "" {
		profileID = prompts.CoordinatorProfileID
	}
	effects, err := surfacecatalog.AdmittedCommandEffects(profiles, profileID, surfaceID)
	testutil.FailErr(t, "admitted command effects", err)
	held := map[string]bool{}
	for class, markers := range map[toolcontract.CommandEffectClass][]string{
		toolcontract.CommandMutation:      {toolcontract.ReachCommandMutation},
		toolcontract.CommandExternalExec:  {toolcontract.ReachCommandExternalExec, toolcontract.ReachCommandExternalExecSelector},
		toolcontract.CommandNetworkEgress: {toolcontract.ReachCommandNetworkEgress},
	} {
		want, err := toolcontract.NewCommandEffects(class)
		testutil.FailErr(t, "command effect", err)
		if effects.Allows(want) {
			for _, m := range markers {
				held[m] = true
			}
		}
	}
	return held
}

// hostText is the text the host wrote into a message: host-origin or
// system-authority parts, the whole message when it carries no parts, and the
// whole result of a refused call, which ran no tool.
func hostText(m api.Message) []string {
	host := func(origin api.MessageOrigin, authority api.ContentAuthority) bool {
		return origin == api.MessageOriginHost || authority == api.ContentAuthoritySystem || authority == api.ContentAuthorityDeveloper
	}
	if m.ToolResult != nil && m.ToolResult.Outcome == api.ToolResultOutcomeRejected {
		return []string{m.Content}
	}
	if len(m.ContentParts) == 0 {
		if m.Role == api.MessageRoleSystem || host(m.Origin, m.Authority) {
			return []string{m.Content}
		}
		return nil
	}
	var out []string
	for _, part := range m.ContentParts {
		if host(part.Origin, part.Authority) {
			out = append(out, part.Content)
		}
	}
	return out
}

// check lists what one request's host text names that the request does not
// offer: a tool, operation or command program outside its tools and command
// reach; and an argument attributed to a tool, as `tool(arg=…)` or
// `tool.arg`, that the tool's schema as sent lacks.
func (o *reachOracle) check(t *testing.T, req modelcall.CompletionRequest, profiles []sandbox.ToolProfile) []string {
	t.Helper()
	offered := map[string]bool{}
	sent := map[string]map[string]any{}
	for _, meta := range req.Tools {
		offered[meta.Name] = true
		sent[meta.Name] = meta.ArgsSchema
	}
	schemaSent := func(tool string) (map[string]any, bool) {
		s, ok := sent[tool]
		return s, ok
	}
	if offered["command"] {
		maps.Copy(offered, commandReach(t, profiles, req.Debug.ProfileID, req.Debug.Surface))
	}
	callToolByID := map[string]string{}
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Name != "" {
				callToolByID[tc.ID] = tc.Name
			}
		}
	}
	isZeroToolUtility := req.Composition != "" && req.Composition != modelcall.CompositionConversation && len(req.Tools) == 0
	var out []string
	for i, m := range req.Messages {
		for _, text := range hostText(m) {
			if isZeroToolUtility {
				for _, name := range toolvocab.ToolsIn(o.names, text) {
					out = append(out, fmt.Sprintf("message %d (%s %s) names %s in zero-tool utility request: %s", i, m.Role, m.Kind, name, lineWith(text, name)))
				}
				continue
			}
			reachText := text
			if m.Role == api.MessageRoleTool {
				var rejectedTool string
				if m.ToolResult != nil && m.ToolResult.Outcome == api.ToolResultOutcomeRejected {
					rejectedTool = m.ToolResult.Tool
					if rejectedTool == "" {
						rejectedTool = callToolByID[m.ToolResult.ToolCallID]
					}
				}
				reachText = maskRejectedToolEcho(text, rejectedTool)
			}
			for _, name := range toolvocab.UnreachableNames(o.names, reachText, offered) {
				out = append(out, fmt.Sprintf("message %d (%s %s) names %s, which the request does not offer: %s", i, m.Role, m.Kind, name, lineWith(reachText, name)))
			}
			for _, claim := range toolvocabtest.UnacceptedArgs(o.names, text, schemaSent) {
				out = append(out, fmt.Sprintf("message %d (%s %s) gives %s argument `%s`, which its schema as sent lacks: %s", i, m.Role, m.Kind, claim.Tool, claim.Arg, lineWith(text, claim.Arg)))
			}
		}
	}
	return out
}

// maskRejectedToolEcho replaces bare mentions of a rejected result's own tool,
// which name the result's subject rather than claim reach. A call to that tool
// is not a bare mention and is still checked.
func maskRejectedToolEcho(text, rejectedTool string) string {
	if rejectedTool == "" {
		return text
	}
	return strings.ReplaceAll(text, "`"+rejectedTool+"`", "the rejected tool")
}

// lineWith quotes the line that names name, preferring a backticked mention.
func lineWith(text, name string) string {
	lines := strings.Split(text, "\n")
	for _, want := range []string{"`" + name, name} {
		for _, line := range lines {
			if strings.Contains(line, want) {
				return fmt.Sprintf("%q", strings.TrimSpace(line))
			}
		}
	}
	return "(no single line)"
}

// reachCell is one surface (or worker agent), workflow phase, posture and
// tool-loading state a recorded request was sent in.
type reachCell struct {
	Surface, Phase, Posture, Loading string
}

// reachScenario is a scripted session over one approval posture, workflow
// phase and loading state. The prompt "@request" loads a tool the surface
// keeps deferred.
type reachScenario struct {
	loading  string
	posture  api.SessionPosture
	approval gate.Posture
	workflow string
	decider  decide.Decider
	prompts  []string
	cells    []string
}

const (
	loadResponse = 7
	useResponse  = 8
)

func reachResponses() []llm.MockResponseEntry {
	return []llm.MockResponseEntry{
		{Pattern: "what is here", FollowUpText: "A small Go module."},
		{
			Pattern:      "parallel modules",
			ToolCalls:    []llm.MockToolCall{{ID: "dispatch", Name: "task", Args: TaskToolArgs("implementer", "Add module scaffold under pkg/a", "pkg/a/**")}},
			FollowUpText: "Dispatched implementer for module A.",
		},
		{
			Pattern:      "Add module scaffold",
			ToolCalls:    []llm.MockToolCall{{ID: "scaffold", Name: "command", Args: map[string]any{"command": "mkdir -p pkg/a && printf 'package a\\n' > pkg/a/a.go"}}},
			FollowUpText: "Added module A.",
		},
		{Pattern: "write it directly", ToolCalls: []llm.MockToolCall{{ID: "unoffered", Name: "write", Args: map[string]any{"path": "notes.md", "content": "x"}}}, FollowUpText: "Tried."},
		{Pattern: "run nothing", ToolCalls: []llm.MockToolCall{{ID: "noargs", Name: "command", Args: map[string]any{}}}, FollowUpText: "Tried."},
		{Pattern: "count with a cap", ToolCalls: []llm.MockToolCall{{ID: "badarg", Name: "update_progress", Args: map[string]any{"max_results": 3}}}, FollowUpText: "Tried."},
		{Pattern: "write a note", ToolCalls: []llm.MockToolCall{{ID: "mutate", Name: "command", Args: map[string]any{"command": "printf 'x\\n' > note.txt"}}}, FollowUpText: "Wrote it."},
		loadResponse: {Pattern: "load what is missing", FollowUpText: "Loaded."},
		useResponse:  {Pattern: "use what was loaded", FollowUpText: "Used it."},
	}
}

// Every tool, operation and command program that host text names in a
// request production sends is in that request's own tools or command reach,
// and every argument a refusal's recovery names is in a named tool's schema as
// sent. A preloaded skill is host text, so its tools are checked the same way.
func TestEveryRequestNamesOnlyWhatItOffers(t *testing.T) {
	oracle := newReachOracle(t)
	scenarios := []reachScenario{
		{
			loading: "nothing loaded", approval: gate.PostureBalanced, decider: decide.Absent{},
			prompts: []string{"what is here", "build parallel modules with implementer"},
			cells:   []string{"implement_investigate", surface.SurfaceImplementPark, "implementer"},
		},
		{
			loading: "every unit loaded", approval: gate.PostureBalanced, decider: decidetest.LoadAll{},
			prompts: []string{"what is here", "build parallel modules with implementer"},
			cells:   []string{"implement_investigate", surface.SurfaceImplementPark},
		},
		{
			loading: "deferred, requested, then loaded earlier", approval: gate.PostureBalanced, decider: &decidetest.Fake{},
			prompts: []string{"what is here", "@request", "use what was loaded"},
			cells:   []string{"implement_investigate"},
		},
		{
			loading: "deferred under spec posture", posture: api.SessionPostureSpec, approval: gate.PostureBalanced, decider: &decidetest.Fake{},
			prompts: []string{"what is here", "@request", "use what was loaded"},
			cells:   []string{"implement_investigate"},
		},
		{
			loading: "refused calls", approval: gate.PostureBalanced, decider: decide.Absent{},
			prompts: []string{"write it directly", "run nothing", "count with a cap"},
			cells:   []string{"implement_investigate"},
		},
		{loading: "nothing loaded", approval: gate.PostureStrict, decider: decide.Absent{}, prompts: []string{"what is here"}, cells: []string{"implement_investigate"}},
		{loading: "nothing loaded", approval: gate.PostureLight, decider: decide.Absent{}, prompts: []string{"what is here", "write a note"}, cells: []string{"implement_investigate"}},
		{loading: "nothing loaded", approval: gate.PostureBalanced, workflow: "recon-pack", decider: decide.Absent{}, prompts: []string{"what is here"}, cells: []string{"orchestrate_plan"}},
	}
	counts := map[reachCell]int{}
	var codes []string
	for _, sc := range scenarios {
		t.Run(fmt.Sprintf("%s/%s/%s", sc.approval, sc.workflow, sc.loading), func(t *testing.T) {
			got := runReachScenario(t, oracle, sc)
			var seen []string
			for cell, n := range got.counts {
				counts[cell] += n
				seen = append(seen, cell.Surface)
			}
			for _, want := range sc.cells {
				if !slices.Contains(seen, want) {
					t.Errorf("no request was recorded on %s; recorded %v", want, seen)
				}
			}
			if len(got.counts) == 0 {
				t.Error("the scenario recorded no request")
			}
			codes = append(codes, got.codes...)
			t.Logf("refusals: %v", got.codes)
			if len(got.violations) > 0 {
				t.Errorf("%d names the request does not offer:\n%s", len(got.violations), strings.Join(got.violations, "\n"))
			}
		})
	}
	cells := slices.SortedFunc(maps.Keys(counts), func(a, b reachCell) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	for _, cell := range cells {
		t.Logf("%d requests on %s, phase %s, %s, %s", counts[cell], cell.Surface, cell.Phase, cell.Posture, cell.Loading)
	}
	slices.Sort(codes)
	t.Logf("refusals checked: %v", slices.Compact(codes))
	if len(codes) == 0 {
		t.Error("no refusal reached a request")
	}
}

type reachResult struct {
	counts     map[reachCell]int
	codes      []string
	violations []string
}

// loadSpan marks the conversation requests sent before, during and after the
// turn that requests a deferred tool.
type loadSpan struct {
	tool          string
	before, after int
}

// loadingTransitions lists where the requests themselves contradict the
// loading states a scenario scripts: the tool is withheld until it is
// requested, the request that asks for it does not carry it, the turn's last
// request carries it, and every later turn keeps it.
func loadingTransitions(oracle *reachOracle, requests []modelcall.CompletionRequest, span loadSpan) []string {
	offers := func(req modelcall.CompletionRequest) bool {
		return slices.ContainsFunc(req.Tools, func(meta tools.ToolMeta) bool { return meta.Name == span.tool })
	}
	var out []string
	for i, req := range requests {
		switch {
		case i < span.before && offers(req):
			out = append(out, fmt.Sprintf("request %d offers %s before it is requested", i, span.tool))
		case i == span.before && offers(req):
			out = append(out, fmt.Sprintf("request %d asks for %s while already offering it", i, span.tool))
		case i == span.after-1 && !offers(req):
			out = append(out, fmt.Sprintf("request %d, the last of the requesting turn, does not offer %s", i, span.tool))
		case i >= span.after && !offers(req):
			out = append(out, fmt.Sprintf("request %d, a later turn, no longer offers %s", i, span.tool))
		}
	}
	// The requesting turn needs the asking request and one after its result.
	if span.after-span.before < 2 || span.after >= len(requests) {
		out = append(out, fmt.Sprintf("requests %d..%d of %d leave a loading state unrecorded", span.before, span.after, len(requests)))
		return out
	}
	out = append(out, verifyPrePostLoadSchemaParity(oracle, requests[:span.after-1], requests[span.after-1], span.tool)...)
	if postA, okA := toolMetaSchemaInRequest(requests[span.after-1], span.tool); okA {
		if postB, okB := toolMetaSchemaInRequest(requests[span.after], span.tool); okB && !reflect.DeepEqual(postA, postB) {
			out = append(out, fmt.Sprintf("loaded schema for %s drifted between requests %d and %d", span.tool, span.after-1, span.after))
		}
	}
	return out
}

func toolMetaSchemaInRequest(req modelcall.CompletionRequest, toolName string) (map[string]any, bool) {
	for _, meta := range req.Tools {
		if meta.Name == toolName {
			return meta.ArgsSchema, meta.ArgsSchema != nil
		}
	}
	return nil, false
}

// verifyPrePostLoadSchemaParity checks that any argument or enum constraint
// advertised for toolName in pre-load host messages is accepted by the actual
// post-load CompletionRequest.Tools schema as sent (including under narrowing).
func verifyPrePostLoadSchemaParity(
	oracle *reachOracle,
	preRequests []modelcall.CompletionRequest,
	postRequest modelcall.CompletionRequest,
	toolName string,
) []string {
	postSchema, ok := toolMetaSchemaInRequest(postRequest, toolName)
	if !ok {
		return []string{fmt.Sprintf("post-load request lacks non-nil ArgsSchema for %s", toolName)}
	}
	lookup := func(tool string) (map[string]any, bool) {
		if tool == toolName {
			return postSchema, true
		}
		return nil, false
	}
	var out []string
	for rIdx, req := range preRequests {
		for mIdx, m := range req.Messages {
			for _, text := range hostText(m) {
				for _, bad := range toolvocabtest.UnacceptedArgs(oracle.names, text, lookup) {
					out = append(out, fmt.Sprintf(
						"pre-load request %d message %d advertises %s argument `%s` absent from post-load schema",
						rIdx, mIdx, bad.Tool, bad.Arg,
					))
				}
			}
		}
	}
	return out
}

func runReachScenario(t *testing.T, oracle *reachOracle, sc reachScenario) reachResult {
	t.Helper()
	config := &llm.MockConfig{Responses: reachResponses()}
	rec := llmtest.NewRecordingClient(llm.NewMockProvider(config))
	h := BuildForTest(t, WithLLMClient(rec), WithDecider(sc.decider), withGlobalApprovalPosture(sc.approval))
	ctx := context.Background()
	h.Start(t)
	dir := t.TempDir()
	WriteTestProjectApprovalsAllowWrite(t, dir)
	_ = WriteImplementFixture(t, dir)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: sc.posture}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	phase := "work"
	if sc.workflow != "" {
		run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
			WorkflowID: sc.workflow, WorkflowVersion: activeWorkflowVersion(sc.workflow),
		})
		testutil.FailErr(t, "start "+sc.workflow, err)
		phase = run.CurrentPhase
	}
	conversation := func() []modelcall.CompletionRequest { return conversationRequests(rec) }
	var span *loadSpan
	for _, prompt := range sc.prompts {
		requesting := prompt == "@request"
		if requesting {
			span = &loadSpan{tool: requestableTool(t, rec), before: len(conversation())}
			prompt = config.Responses[loadResponse].Pattern
			config.Responses[loadResponse].ToolCalls = []llm.MockToolCall{{ID: "load", Name: "request_tools", Args: map[string]any{"need": span.tool}}}
		}
		_, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, prompt)
		testutil.FailErr(t, "prompt "+prompt, err)
		if requesting {
			span.after = len(conversation())
		}
	}
	// A dispatched worker's requests are sent only once its queued job runs; a
	// scenario that reads the requests before then has not seen that surface.
	testutil.FailErr(t, "drain worker jobs", DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID))
	FailOnEndedWorker(t, h, ctx, sess.ProjectID)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	stored, err := h.Store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "read the session", err)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)
	out := reachResult{counts: map[reachCell]int{}}
	allRecorded := rec.AllRequests()
	for uIdx, uReq := range allRecorded {
		if uReq.Composition == modelcall.CompositionConversation {
			continue
		}
		if len(uReq.Tools) > 0 {
			out.violations = append(out.violations, fmt.Sprintf("utility request %d (%s) carries %d tools", uIdx, uReq.Composition, len(uReq.Tools)))
		}
		for _, v := range oracle.check(t, uReq, profiles) {
			out.violations = append(out.violations, fmt.Sprintf("utility request %d (%s): %s", uIdx, uReq.Composition, v))
		}
	}
	requests := conversation()
	if span != nil {
		out.violations = append(out.violations, loadingTransitions(oracle, requests, *span)...)
	}
	for i, req := range requests {
		where := req.Debug.Surface
		if where == "" {
			where = req.Debug.AgentType
		}
		out.counts[reachCell{Surface: where, Phase: phase, Posture: fmt.Sprintf("%s session, %s approval", stored.Posture, sc.approval), Loading: sc.loading}]++
		for _, m := range req.Messages {
			if m.ToolResult != nil && m.ToolResult.Outcome == api.ToolResultOutcomeRejected {
				out.codes = append(out.codes, m.ToolResult.Codes...)
			}
		}
		found := oracle.check(t, req, profiles)
		for _, v := range found {
			out.violations = append(out.violations, fmt.Sprintf("request %d on %s: %s", i, where, v))
		}
		if len(found) > 0 {
			names := make([]string, 0, len(req.Tools))
			for _, meta := range req.Tools {
				names = append(names, meta.Name)
			}
			out.violations = append(out.violations, fmt.Sprintf("request %d on %s offers %v", i, where, names))
		}
	}
	return out
}

// requestableTool is a tool investigate keeps deferred that the last request
// does not carry.
func requestableTool(t *testing.T, rec *llmtest.RecordingClient) string {
	t.Helper()
	carried := map[string]bool{}
	for _, meta := range rec.LastRequest().Tools {
		carried[meta.Name] = true
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 1)
	testutil.FailErr(t, "compile the investigate plan", err)
	for _, name := range plan.DeferredNames() {
		if !carried[name] {
			return name
		}
	}
	t.Fatal("investigate carries every tool it defers")
	return ""
}

// The oracle fails on each kind of name a request can carry without offering
// it, and passes the same text reworded.
func TestRequestReachFailsOnSeededRequests(t *testing.T) {
	oracle := newReachOracle(t)
	tool := func(name string, args ...string) tools.ToolMeta {
		props := map[string]any{}
		for _, arg := range args {
			props[arg] = map[string]any{"type": "string"}
		}
		return tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object", "properties": props}}
	}
	refused := func(text string) api.Message {
		return api.Message{Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: text,
			ToolResult: &api.ToolResult{Tool: "update_progress", Outcome: api.ToolResultOutcomeRejected}}
	}
	system := func(text string) api.Message {
		return api.Message{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, Content: text}
	}
	// command's schema here is the one without timeout_ms, as after the
	// parameter is dropped; wait's schema allows only http_ready and port_ready.
	waitNarrowed := tools.ToolMeta{
		Name: "wait",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout_ms": map[string]any{"type": "integer"},
				"conditions": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind": map[string]any{"type": "string", "enum": []any{"http_ready", "port_ready"}},
							"url":  map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}
	askUserMeta := tools.ToolMeta{
		Name: "ask_user",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			},
		},
	}
	offered := []tools.ToolMeta{
		tool("update_progress", "content", "expected_revision"),
		askUserMeta,
		tool("command", "command"),
		waitNarrowed,
	}
	for name, tc := range map[string]struct {
		message api.Message
		want    int
	}{
		"guide names an unoffered tool":                  {message: system("Store the checklist, then call `promote_overlay`."), want: 1},
		"guide reworded":                                 {message: system("Keep the checklist current with `update_progress`."), want: 0},
		"recovery names an unoffered tool":               {message: refused("Rejected: stale revision.\nFix: call `edit` on the board."), want: 1},
		"recovery names an unknown argument":             {message: refused("Rejected: stale revision.\nFix: pass `path` to `update_progress`."), want: 1},
		"recovery reworded":                              {message: refused("Rejected: stale revision.\nFix: resend `update_progress` with the current `expected_revision`."), want: 0},
		"user text is not host text":                     {message: api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "call `promote_overlay`"}, want: 0},
		"guide gives command a dropped argument":         {message: system("Run it with `command(command=…, timeout_ms=60000)`."), want: 1},
		"guide names a dropped dotted argument":          {message: system("Raise `command.timeout_ms` for slow builds."), want: 1},
		"guide gives command an argument never schemaed": {message: system("Run it with `command(command=…, wait_ms=5)`."), want: 1},
		"guide gives command its own argument":           {message: system("Run it with `command(command=…)`."), want: 0},
		"guide call reworded":                            {message: system("Retry with `update_progress(content=…, expected_revision=…)`."), want: 0},
		"cross-tool questions attributed to command":     {message: system("Run `command(command=\"ls\", questions=[\"proceed?\"])`."), want: 1},
		"cross-tool questions reworded":                  {message: system("Run `command(command=\"ls\")` and ask via `ask_user(questions=[…])`."), want: 0},
		"narrowed wait enum process_done":                {message: system("Await completion via `wait(timeout_ms=5000, conditions=[{\"kind\":\"process_done\"}])`."), want: 1},
		"narrowed wait enum http_ready reworded":         {message: system("Await service via `wait(timeout_ms=5000, conditions=[{\"kind\":\"http_ready\",\"url\":\"http://127.0.0.1:8080\"}])`."), want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			req := modelcall.CompletionRequest{Tools: offered, Messages: []api.Message{tc.message}, Debug: modelcall.RequestDebug{ProfileID: prompts.CoordinatorProfileID, Surface: surface.SurfaceImplementPark}}
			if got := oracle.check(t, req, oracle.profiles); len(got) != tc.want {
				t.Fatalf("%d findings, want %d:\n%s", len(got), tc.want, strings.Join(got, "\n"))
			}
		})
	}
}
