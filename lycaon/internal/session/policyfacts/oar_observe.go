package policyfacts

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"maps"
	"slices"
	"strconv"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

var sandboxPostInvokeCodes = []string{
	isolation.CodeBoundaryRefused,
	isolation.CodeRemotePackageDestinationDenied,
	toolrejection.VerifyUnverifiableCode,
}

// ObserveConfine publishes post-invoke boundary facts.
func ObserveConfine(gc *oar.GuardContext, obs confine.Observation) {
	if gc == nil || !obs.Applied {
		return
	}
	gc.Execution.ConfineApplied = true
	gc.Execution.NetworkMode = obs.Network
	gc.Execution.DenialSubject = obs.DenialSubject
	gc.Execution.ConfineSignals = append([]string(nil), obs.Signals...)
	gc.Execution.FailedStages = append([]string(nil), obs.FailedStages...)
	gc.Execution.ProcessRunning = obs.Running
	observeSandboxRefusals(gc, obs.Refusals)
	data := map[string]any{"tool": gc.Invocation.Tool}
	if dest := obs.Destination; dest != "" {
		data["destination"] = dest
	}
	if obs.Port > 0 {
		data["port"] = strconv.Itoa(obs.Port)
	}
	for _, code := range sandboxPostInvokeCodes {
		gc.PutRejectData(code, data)
	}
}

// observeSandboxRefusals publishes what the kernel refused, grouped by the
// capability whose rule would admit it. Refused paths carry the grant each
// layer's recovery would request; control-plane paths have none.
func observeSandboxRefusals(gc *oar.GuardContext, refusals confine.SandboxRefusals) {
	for _, refusal := range refusals.Refusals {
		gc.Execution.SandboxRefusals = append(gc.Execution.SandboxRefusals, refusal.Display())
		switch refusal.Recovery {
		case confine.RecoverSocketPath:
			gc.Refusals.RefusedSocketPaths = appendUnique(gc.Refusals.RefusedSocketPaths, refusal.Grant)
		case confine.RecoverOutboundPort:
			gc.Refusals.RefusedConnectPorts = appendUnique(gc.Refusals.RefusedConnectPorts, refusal.Port())
		case confine.RecoverLocalListen:
			gc.Refusals.RefusedListenPorts = appendUnique(gc.Refusals.RefusedListenPorts, refusal.Port())
		case confine.RecoverProcessControl:
			gc.Refusals.RefusedSignals = append(gc.Refusals.RefusedSignals, refusal.Display())
		case confine.RecoverHostExecution:
			gc.Refusals.UnsandboxedRefusals = append(gc.Refusals.UnsandboxedRefusals, refusal.Display())
		case confine.RecoverWriteRoot, confine.RecoverReadPath, confine.RecoverNone:
			// Filesystem layers and their grants are grouped below.
		}
	}
	for _, path := range refusals.Paths(confine.AccessWrite) {
		gc.Refusals.RefusedWritePaths = append(gc.Refusals.RefusedWritePaths, path.Path)
		if path.Grant != "" {
			gc.Refusals.RefusedWriteGrants = append(gc.Refusals.RefusedWriteGrants, path.Grant)
		}
	}
	for _, path := range refusals.Paths(confine.AccessRead) {
		gc.Refusals.RefusedReadPaths = append(gc.Refusals.RefusedReadPaths, path.Path)
		if path.Grant != "" {
			gc.Refusals.RefusedReadGrants = append(gc.Refusals.RefusedReadGrants, path.Grant)
		}
	}
	gc.Refusals.RefusedWriteGrants = confine.CoveringGrants(gc.Refusals.RefusedWriteGrants)
	gc.Refusals.RefusedReadGrants = confine.CoveringGrants(gc.Refusals.RefusedReadGrants)
}

func appendUnique(list []string, value string) []string {
	if value == "" || slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

// RegisterWorktreeFacts compares the command's Git index capture only when a
// selected rule reads one of the facts ([OAR-FACT-11]).
func RegisterWorktreeFacts(ctx context.Context, gc *oar.GuardContext, snapshot indexwatch.Snapshot) {
	if gc == nil || !snapshot.Active() {
		return
	}
	var once sync.Once
	var compareErr error
	compare := func(target *oar.GuardContext) error {
		once.Do(func() {
			classify := confine.AgentPolicyClassifier(snapshot.Root())
			keep := func(abs string) bool {
				_, ok := classify(abs)
				return ok
			}
			result, err := snapshot.Stale(ctx, keep)
			compareErr = err
			target.Source.WorktreeStalePaths = result.Stale
			target.Source.WorktreeLeftoverPaths = result.Leftover
			target.Source.WorktreeConflictPaths = result.Conflicted
		})
		return compareErr
	}
	for _, fact := range []string{"worktree_stale_paths", "worktree_leftover_paths", "worktree_conflict_paths"} {
		gc.RegisterProvider("paintedwolf."+fact, compare)
	}
}

func ObserveSourceParsingFeedback(gc *oar.GuardContext, raised guidance.ToolResultFacts) {
	if gc == nil {
		return
	}
	if raised.HasCode(toolrejection.SourceAnalysisUnavailableCode) {
		gc.Source.SourceAnalysisUnavailable = true
		gc.PutRejectData(toolrejection.SourceAnalysisUnavailableCode, raised.FeedbackFor(toolrejection.SourceAnalysisUnavailableCode).Details)
	}
	if raised.HasCode(tools.SyntaxCheckOverriddenCode) {
		gc.Source.SyntaxCheckOverridden = true
		gc.PutRejectData(tools.SyntaxCheckOverriddenCode, syntaxOverrideDetails(raised))
	}
}

func syntaxOverrideDetails(raised guidance.ToolResultFacts) map[string]any {
	details := maps.Clone(raised.FeedbackFor(tools.SyntaxCheckOverriddenCode).Details)
	if details == nil {
		details = map[string]any{}
	}
	var paths []string
	seen := map[string]bool{}
	add := func(path string) {
		if path != "" && !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	for _, item := range raised.Feedback {
		if item.Code != tools.SyntaxCheckOverriddenCode {
			continue
		}
		switch values := item.Details["paths"].(type) {
		case []string:
			for _, path := range values {
				add(path)
			}
		case []any:
			for _, value := range values {
				if path, ok := value.(string); ok {
					add(path)
				}
			}
		}
	}
	details["paths"] = paths
	return details
}

// MintedCredentialSource identifies credential-issuing calls.
type MintedCredentialSource interface {
	MintedCredentialRule(action hitl.ProposedAction) (hitl.DetectionMatch, bool)
}

// SetMintedCredentialSource binds the live catalog reader during startup.
func (m *Service) SetMintedCredentialSource(source func() MintedCredentialSource) {
	if m != nil {
		m.mintedCredentials = source
	}
}

// SetRememberSecrets installs the sink for values a lens confirmed.
func (m *Service) SetRememberSecrets(fn secretmatch.RememberFunc) {
	if m != nil {
		m.rememberSecrets = fn
	}
}

// A matched minting rule supplies provenance for harvested credential values.
func (m *Service) ObserveMintedCredential(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string) {
	if m == nil || m.mintedCredentials == nil || m.rememberSecrets == nil || sess == nil || output == "" {
		return
	}
	source := m.mintedCredentials()
	if source == nil {
		return
	}
	hit, ok := source.MintedCredentialRule(hitl.ProposedAction{
		Tool:       tool,
		Args:       args,
		ProjectDir: m.workspace.SettingsPath(ctx, sess),
		SessionID:  sess.ID,
	})
	if !ok {
		return
	}
	values := secretharvest.RememberMinted(hit.RuleID, hit.RuleTitle, tool, output)
	if len(values) == 0 {
		return
	}
	m.rememberSecrets(sessiontree.RootID(ctx, m.store, sess.ID), values)
}

// ObserveEditorConfigMismatch publishes a tool-stated mismatch and its details
// to post-tool policy.
func ObserveEditorConfigMismatch(gc *oar.GuardContext, raised guidance.ToolResultFacts) {
	if gc == nil || !raised.HasCode(toolrejection.EditorConfigMismatchCode) {
		return
	}
	gc.Source.EditorConfigMismatch = true
	gc.PutRejectData(toolrejection.EditorConfigMismatchCode, raised.FeedbackFor(toolrejection.EditorConfigMismatchCode).Details)
}

// ObserveHTTPRequestWebPage publishes a web page http_request delivered and
// its details to post-tool policy.
func ObserveHTTPRequestWebPage(gc *oar.GuardContext, raised guidance.ToolResultFacts) {
	if gc == nil || !raised.HasCode(toolrejection.HTTPRequestWebPageCode) {
		return
	}
	gc.Invocation.HTTPRequestWebPage = true
	gc.PutRejectData(toolrejection.HTTPRequestWebPageCode, raised.FeedbackFor(toolrejection.HTTPRequestWebPageCode).Details)
}

// deferredUnactivatedCount is how many of surfaceID's deferred tools this
// session has not loaded.
func (m *Service) DeferredUnactivatedCount(sessionID, surfaceID string) int64 {
	if m == nil || surfaceID == "" {
		return 0
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
	if err != nil {
		return 0
	}
	deferred := plan.DeferredNames()
	active := m.loading.Active(sessionID)
	var n int64
	for _, name := range deferred {
		if !active[name] {
			n++
		}
	}
	return n
}

// observeDeferredUnactivated publishes the count for the session's turn surface.
func (m *Service) ObserveDeferredUnactivated(gc *oar.GuardContext, sessionID string) {
	if m == nil || gc == nil {
		return
	}
	surfaceID := m.surface.PromptTurnSurfaceID(sessionID)
	gc.SetDeferredUnactivated(m.DeferredUnactivatedCount(sessionID, surfaceID))
}
