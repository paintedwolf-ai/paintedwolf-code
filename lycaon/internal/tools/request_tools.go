package tools

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// RequestToolsBoundary is the profile-deferral view request_tools validates against.
// ToolDeferred matches exact names and trailing-* patterns.
type RequestToolsBoundary interface {
	AssertToolAllowed(ctx context.Context, profileID, toolName string, access sandbox.ToolAccess) error
	ToolDeferred(profileID, toolName string, access sandbox.ToolAccess) bool
}

// RequestResolver turns the model's description of what it needs into the
// loadable schemas to offer. The session layer supplies the decision engine.
type RequestResolver func(ctx context.Context, tctx ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome

// RequestObserver records a resolution after surface filtering and activation.
type RequestObserver func(context.Context, ToolContext, turnload.RequestOutcome, turnload.RequestToolsResult, time.Duration)

// RequestToolsDeps configures request_tools registration.
type RequestToolsDeps struct {
	Activation SchemaActivation
	Boundary   RequestToolsBoundary
	// Resolve ranks loadable schemas against the request text. Nil loads
	// explicitly named schemas and answers other text with the catalog.
	Resolve RequestResolver
	Record  RequestObserver
	// RejectFmt is loaded after native registration.
	RejectFmt func() *guidance.StaticRejectFormatter
}

// RegisterRequestTools registers the meta-tool that loads open-world schemas.
func RegisterRequestTools(reg *DefaultRegistry, deps RequestToolsDeps) error {
	if reg == nil || deps.Activation == nil || deps.Boundary == nil {
		return fmt.Errorf("registry, activation store, and boundary required")
	}
	t := requestTools{reg: reg, deps: deps}
	return reg.Register("request_tools", t.run)
}

type requestTools struct {
	reg  *DefaultRegistry
	deps RequestToolsDeps
}

func (t requestTools) run(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	need, _ := args["need"].(string)
	need = strings.TrimSpace(need)
	if need == "" {
		return "", RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "need is required"})
	}
	need, cursor, err := turnload.ParseDiscoveryNeed(need)
	if err != nil {
		return "", RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": err.Error()})
	}
	browsing := cursor != ""
	started := time.Now()
	profileID := strings.TrimSpace(tctx.Agent)
	if profileID == "" {
		profileID = DefaultToolProfileID
	}
	plan := tctx.TurnToolPlan
	constrained := strings.TrimSpace(tctx.TurnSurfaceID) != "" || plan.Compiled()
	requestable := liveRequestableMetas(t.deps, t.reg, profileID, tctx.ToolAccess, plan, constrained)
	cards := make([]turnload.ToolCard, 0, len(requestable))
	active := t.deps.Activation.Active(tctx.SessionID)
	for _, meta := range requestable {
		if active[meta.Name] {
			continue
		}
		cards = append(cards, turnload.ToolCard{Name: meta.Name, Description: meta.Description})
	}
	outcome := turnload.RequestOutcome{Need: need, Abstained: browsing}
	if browsing {
		outcome.Reason = "catalog browsing"
	}
	result := turnload.RequestToolsResult{Need: need}
	if !browsing {
		outcome = t.resolve(ctx, tctx, need, cards)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		result = t.activationResult(ctx, tctx, profileID, plan, constrained, outcome)
	}
	if !browsing && len(result.Loaded)+len(result.AlreadyLoaded) == 0 && (outcome.Failure == "" || len(cards) == 0) {
		if t.deps.Record != nil {
			t.deps.Record(ctx, tctx, outcome, result, time.Since(started))
		}
		available := make([]string, 0, len(requestable))
		for _, meta := range requestable {
			available = append(available, meta.Name)
		}
		return "", formatRequestToolsReject(t.deps, profileID, need, available)
	}
	if browsing || outcome.Failure != "" {
		status := "ranking_unavailable"
		if browsing {
			status = "catalog"
		}
		entries := make([]turnload.DiscoveryEntry, 0, len(cards))
		for _, card := range cards {
			if slices.Contains(result.Loaded, card.Name) || slices.Contains(result.AlreadyLoaded, card.Name) {
				continue
			}
			entries = append(entries, turnload.DiscoveryEntry(card))
		}
		result.Discovery, err = DiscoveryPage(tctx, "request_tools", need, cursor, status, outcome.Failure, entries)
		if err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !browsing && len(result.Loaded)+len(result.AlreadyLoaded) > 0 {
		t.deps.Activation.Activate(tctx.SessionID, result.Loaded, need)
		result.Note = "Loaded schemas are on the next model call; this turn continues automatically."
	}
	if t.deps.Record != nil {
		t.deps.Record(ctx, tctx, outcome, result, time.Since(started))
	}
	raw, err := surveyjson.Marshal(result)
	return string(raw), err
}

// Exact identifiers do not need scores for unrelated registered tools.
func (t requestTools) resolve(ctx context.Context, tctx ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
	for _, meta := range t.reg.List() {
		if strings.EqualFold(need, meta.Name) {
			return turnload.RequestOutcome{Need: need, Exact: []string{meta.Name}}
		}
	}
	if t.deps.Resolve != nil {
		return t.deps.Resolve(ctx, tctx, need, cards)
	}
	return turnload.ResolveRequest(ctx, nil, turnload.RequestSpec{}, need, cards)
}

func (t requestTools) activationResult(ctx context.Context, tctx ToolContext, profileID string, plan toolsurface.Plan, constrained bool, outcome turnload.RequestOutcome) turnload.RequestToolsResult {
	// Declared companions load with a selected tool; the plan keeps out any the surface lacks.
	names := toolcontract.WithCompanions(outcome.Loaded())
	registered := make([]turnload.ToolCard, 0)
	for _, meta := range t.reg.List() {
		registered = append(registered, turnload.ToolCard{Name: meta.Name})
	}
	names = append(names, turnload.ExactNames(outcome.Need, registered)...)
	active := t.deps.Activation.Active(tctx.SessionID)
	result := turnload.RequestToolsResult{Need: outcome.Need}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, registered := t.reg.Meta(name); !registered {
			continue
		}
		switch {
		case plan.Deferred(name) && !active[name]:
			result.Loaded = append(result.Loaded, name)
		case plan.Immediate(name) || (plan.Deferred(name) && active[name]):
			result.AlreadyLoaded = append(result.AlreadyLoaded, name)
		case constrained && (plan.Compiled() || toolcontract.IsCatalog(name)):
			continue
		case active[name]:
			result.AlreadyLoaded = append(result.AlreadyLoaded, name)
		case t.deps.Boundary.ToolDeferred(profileID, name, tctx.ToolAccess):
			result.Loaded = append(result.Loaded, name)
		case t.deps.Boundary.AssertToolAllowed(ctx, profileID, name, tctx.ToolAccess) == nil:
			result.AlreadyLoaded = append(result.AlreadyLoaded, name)
		}
	}
	sort.Strings(result.Loaded)
	sort.Strings(result.AlreadyLoaded)
	for _, name := range result.Loaded {
		if _, nearest := outcome.Nearest[name]; nearest {
			result.Nearest = append(result.Nearest, name)
		}
	}
	return result
}

func formatRequestToolsReject(deps RequestToolsDeps, profileID, need string, available []string) error {
	sort.Strings(available)
	var fmtr *guidance.StaticRejectFormatter
	if deps.RejectFmt != nil {
		fmtr = deps.RejectFmt()
	}
	return FormatDecisionReject("TOOL_REQUEST_UNMATCHED", map[string]any{
		"need":      need,
		"available": available,
		"profile":   profileID,
	}, fmtr)
}

func liveRequestableMetas(deps RequestToolsDeps, reg *DefaultRegistry, profileID string, access sandbox.ToolAccess, plan toolsurface.Plan, constrained bool) []ToolMeta {
	if reg == nil {
		return nil
	}
	out := make([]ToolMeta, 0)
	for _, meta := range reg.List() {
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			continue
		}
		if plan.Compiled() {
			if plan.Deferred(name) {
				out = append(out, meta)
			}
			continue
		}
		if constrained && toolcontract.IsCatalog(name) {
			continue
		}
		if deps.Boundary.ToolDeferred(profileID, name, access) {
			out = append(out, meta)
		}
	}
	return out
}
