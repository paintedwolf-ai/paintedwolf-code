package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// CheckpointDependencies defines inputs for wiring checkpoints and brokers.
type CheckpointDependencies struct {
	Database        *db.Store
	Directory       string
	Sessions        *store.SQL
	EventsOutbox    *eventoutbox.Outbox
	EventPublisher  *events.Publisher
	Security        *security.Runtime
	Execution       execution.Runtime
	SettingsService *settings.Service
	Resources       ResourceTracker
}

type unlockRecorder struct{}

func (unlockRecorder) RecordUnlockTx(ctx context.Context, tx *sql.Tx, projectID string, unlock presence.Unlock) error {
	return secretcap.RecordUnlock(ctx, tx, projectID, unlock)
}

// WireCheckpoints wires checkpoints, grant runtimes, brokers, and decisions.
func (r *Runtime) WireCheckpoints(ctx context.Context, deps CheckpointDependencies) error {
	if deps.Security.Authority == nil {
		return fmt.Errorf("authz: capturer required before checkpoint manager wiring")
	}
	checkpointStore := hitl.NewSQLStore(deps.Database)
	checkpointStore.SetEventOutbox(deps.EventsOutbox)
	checkpointMgr := hitl.NewCheckpoints(checkpointStore, deps.EventPublisher, deps.Security.Authority.Recorder)
	deps.Resources.Track("checkpoint-expiries", 25, func(context.Context) error { checkpointMgr.StopExpiryTimers(); return nil })
	checkpointMgr.Sessions.SetSessionAdmission(r.Manager.WithSessionTreeAdmission)
	checkpointMgr.Presence.SetVaultUnlock(deps.Security.Presence, deps.Security.Unlocks, unlockRecorder{})
	deps.Execution.Host.Executor.Secrets.SetPresenceAvailable(checkpointMgr.Presence.PresenceAvailable)
	checkpointMgr.Sessions.SetCheckpointWaitObserver(r.Manager.BeginCheckpointWait)

	var authzRec authzledger.Recorder = deps.Security.Authority.Recorder
	if deps.Execution.Host != nil {
		deps.Execution.Host.Authority.SetAuthzRecorder(authzRec)
	}
	r.Checkpoints = checkpointMgr
	r.Manager.SetSessionCheckpointStop(checkpointMgr)
	r.Manager.SetExecutionCheckpoints(checkpointMgr)
	deps.Execution.Host.Authority.SetCheckpointManager(checkpointMgr)

	writeRootRT := approvalstate.NewSandboxPathGrantRuntime()
	r.SandboxWriteRoot = writeRootRT
	r.Manager.SetSandboxPathGrantRuntime(writeRootRT)
	readPathRT := approvalstate.NewSandboxPathGrantRuntime()
	r.SandboxReadPath = readPathRT
	listenRT := approvalstate.NewSandboxPortGrantRuntime()
	r.SandboxListen = listenRT
	r.Manager.SetSandboxListenRuntime(listenRT)
	loopbackRT := approvalstate.NewSandboxPortGrantRuntime()
	r.SandboxLoopback = loopbackRT
	r.Manager.SetSandboxLoopbackRuntime(loopbackRT)
	loopbackProv := session.NewLoopbackProvenance()
	r.Manager.SetLoopbackProvenance(loopbackProv)

	if err := wireGrantedAccess(r, deps); err != nil {
		return err
	}
	if deps.Security.Harvest != nil {
		r.Manager.SetCredentialFiles(newCredentialFiles(deps.Security.Harvest, deps.Security.Fingerprinter, deps.Database, deps.Security.Capabilities, deps.Sessions))
	}
	wireCredentialObservations(r.Manager, deps.Security.Fingerprinter, deps.Security.Matcher, deps.Security.Harvest)
	deps.Execution.Host.Executor.Secrets.SetSecretExposureSource(func(c context.Context, chatSessionID string) (bool, error) {
		return deps.Sessions.SessionSecretExposure(c, chatSessionID)
	})
	deps.Execution.Host.Executor.Secrets.SetUntrustedIngestionSource(func(c context.Context, chatSessionID string) (bool, error) {
		return deps.Sessions.SessionUntrustedContentResult(c, chatSessionID)
	})
	confine.SetUntrustedIngestionSource(untrustedIngestionStore{store: deps.Sessions})
	deps.Execution.Host.Executor.Network.SetSessionHostLedger(deps.Sessions)

	sensitiveDests, err := approvals.LoadMergedConsequenceBandPaths(deps.Directory)
	if err != nil {
		return fmt.Errorf("consequence-band paths: %w", err)
	}
	deriver := checkpointConsequenceDeriver{dests: sensitiveDests}
	deps.Execution.Host.Executor.Approvals.SetConsequenceDeriver(deriver)
	locations, locErr := sensitivepath.Load(
		sensitivepath.Bundled(),
		sensitivepath.Dir(filepath.Join(deps.Directory, "ask-triggers")),
	)
	if locErr != nil {
		slog.Warn("sensitive locations catalog unavailable", "error", locErr)
	}
	deps.Execution.Host.Commands.SetSandboxWriteRootGate(&session.WriteRootCheckpointBroker{
		Checkpoints:       r.Checkpoints,
		Store:             deps.Sessions,
		Runtime:           writeRootRT,
		ReadRuntime:       readPathRT,
		Consequence:       deriver,
		Authority:         deps.Execution.Host.Authority.ApprovalGate(),
		ApprovalsDisabled: deps.Execution.Host.Authority.ApprovalsDisabled,
		Posture:           deps.Execution.Host.Authority.ApprovalPosture,
		Rule:              deps.Execution.Host.Authority.WriteRootRule,
		Locations:         locations,
		Authz:             authzRec,
	})
	deps.Execution.Host.Authority.SetSandboxListenGate(&session.ListenCheckpointBroker{
		Checkpoints:       r.Checkpoints,
		Store:             deps.Sessions,
		Runtime:           listenRT,
		Loopback:          loopbackRT,
		Authority:         deps.Execution.Host.Authority.ApprovalGate(),
		ApprovalsDisabled: deps.Execution.Host.Authority.ApprovalsDisabled,
		Posture:           deps.Execution.Host.Authority.ApprovalPosture,
		Provenance:        loopbackProv,
		Authz:             authzRec,
	})
	deps.Execution.Host.Authority.SetSandboxLoopbackGate(&session.LoopbackCheckpointBroker{
		Checkpoints:       r.Checkpoints,
		Store:             deps.Sessions,
		Runtime:           loopbackRT,
		Provenance:        loopbackProv,
		Authority:         deps.Execution.Host.Authority.ApprovalGate(),
		ApprovalsDisabled: deps.Execution.Host.Authority.ApprovalsDisabled,
		Posture:           deps.Execution.Host.Authority.ApprovalPosture,
		Authz:             authzRec,
	})
	deps.Execution.Host.Authority.SetLocalNetworkGate(&session.LocalNetworkCheckpointBroker{
		Checkpoints:       r.Checkpoints,
		Store:             deps.Sessions,
		Listen:            listenRT,
		Loopback:          loopbackRT,
		Provenance:        loopbackProv,
		Authority:         deps.Execution.Host.Authority.ApprovalGate(),
		ApprovalsDisabled: deps.Execution.Host.Authority.ApprovalsDisabled,
		Posture:           deps.Execution.Host.Authority.ApprovalPosture,
		Authz:             authzRec,
	})
	toolApprovalRT := wireAskSpamGuards(r, deps.Execution.Host.Authority)
	if deps.SettingsService != nil {
		if err := deps.Security.BuildExceptional(deps.Execution.Host.Executor.Capabilities, deps.SettingsService.Approvals, deps.Execution.Host.Authority.ApprovalsDisabled, r.Manager, deps.Security.Authority.Recorder, r.Manager.SetDirectIPReconstructHook); err != nil {
			return err
		}
	}
	if err := wireToolApprovalCheckpointHooks(checkpointMgr, toolApprovalRT); err != nil {
		return err
	}

	r.Decisions = session.NewSQLDecisionStore(deps.Database)
	r.Manager.SetDecisionStore(r.Decisions)
	return nil
}

func wireAskSpamGuards(r *Runtime, authority *toolhost.AuthorityServices) *approvalstate.ToolApprovalCoalesce {
	toolApprovalRT := approvalstate.NewToolApprovalCoalesce()
	r.Manager.SetToolApprovalCoalesce(toolApprovalRT)
	authority.SetToolApprovalCoalesce(toolApprovalRT)
	gateRepeatRT := approvalstate.NewGateRepeatLedger()
	r.Manager.SetGateRepeatLedger(gateRepeatRT)
	authority.SetGateRepeatLedger(gateRepeatRT)
	r.GateRepeat = gateRepeatRT
	return toolApprovalRT
}

func wireToolApprovalCheckpointHooks(mgr *hitl.Checkpoints, toolApprovalRT *approvalstate.ToolApprovalCoalesce) error {
	mgr.Authority.SetToolApprovalTerminalHook(func(chatSessionID, grantKey string, status hitl.DecisionStatus) {
		toolApprovalRT.ClearPending(chatSessionID, grantKey)
		switch status {
		case hitl.DecisionStatusRejected:
			toolApprovalRT.RecordDeny(chatSessionID, grantKey)
		case hitl.DecisionStatusApproved:
			toolApprovalRT.ClearDeny(chatSessionID, grantKey)
		case hitl.DecisionStatusPending, hitl.DecisionStatusExpired, hitl.DecisionStatusCanceled:
		}
	})
	mgr.Authority.SetToolApprovalRestoreHook(func(row hitl.StoredCheckpoint) {
		chat, _ := row.Payload["coalesce_chat"].(string)
		if strings.TrimSpace(chat) == "" {
			chat = row.SessionID
		}
		key, _ := row.Payload["coalesce_grant_key"].(string)
		joined := 1
		switch value := row.Payload["joined_count"].(type) {
		case int:
			joined = value
		case float64:
			joined = int(value)
		}
		var ids []string
		if values, ok := row.Payload["joined_tool_call_ids"].([]any); ok {
			for _, value := range values {
				if id, ok := value.(string); ok {
					ids = append(ids, id)
				}
			}
		}
		toolApprovalRT.RestorePending(chat, key, row.ID, joined, ids)
	})
	mgr.Authority.SetToolApprovalDenyRestoreHook(func(row hitl.StoredCheckpoint) {
		chat, _ := row.Payload["coalesce_chat"].(string)
		if strings.TrimSpace(chat) == "" {
			chat = row.SessionID
		}
		key, _ := row.Payload["coalesce_grant_key"].(string)
		if strings.TrimSpace(key) != "" {
			toolApprovalRT.RecordDeny(chat, key)
		}
	})
	if err := mgr.RestorePending(context.Background()); err != nil {
		return fmt.Errorf("restore pending checkpoints: %w", err)
	}
	if err := mgr.Authority.RestoreRejectedToolApprovalDenials(context.Background()); err != nil {
		return fmt.Errorf("restore rejected tool-approval denials: %w", err)
	}
	return nil
}

func wireGrantedAccess(r *Runtime, deps CheckpointDependencies) error {
	grantedRT := grantedpath.NewRuntime()
	r.GrantedPath = grantedRT
	approvalGate := deps.Execution.Host.Authority.ApprovalGate()
	grantedRT.SetDurableSource(func(projectID string) []grantedpath.Grant {
		if approvalGate == nil {
			return nil
		}
		return durableGrantedPaths(approvalGate.ListGrants(""), projectID)
	})
	projectpaths.SetGrantedAccessSource(func(rootSession, projectID, abs string, write bool) (projectpaths.Access, bool) {
		mode := grantedpath.ModeRead
		if write {
			mode = grantedpath.ModeWrite
		}
		g, ok := grantedRT.Covers(rootSession, projectID, abs, mode)
		if !ok {
			return projectpaths.Access{}, false
		}
		return projectpaths.Access{Path: g.Path, Tree: g.Tree}, true
	})
	if err := r.Manager.RegisterSessionCleanup("approval-run", 50, func(_ context.Context, sessionID string) error {
		deps.Execution.Host.Authority.ReleaseSessionRun(sessionID)
		r.SandboxReadPath.ReleaseRun(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if err := r.Manager.RegisterSessionDisposal("approvals", 50, func(_ context.Context, sessionID string) error {
		deps.Execution.Host.Authority.ForgetSessionAuthorization(sessionID)
		grantedRT.Forget(sessionID)
		r.SandboxReadPath.ForgetSession(sessionID)
		return nil
	}); err != nil {
		return err
	}
	return r.Manager.RegisterSessionCleanup("harvested-secrets", 53, func(_ context.Context, sessionID string) error {
		if deps.Security.Harvest != nil {
			deps.Security.Harvest.Forget(sessionID)
		}
		return nil
	})
}

func durableGrantedPaths(leases []hitl.ApprovalGrant, projectID string) []grantedpath.Grant {
	var out []grantedpath.Grant
	for _, lease := range leases {
		if lease.GrantedPath == nil || strings.TrimSpace(lease.GrantedPath.Path) == "" {
			continue
		}
		switch lease.Scope {
		case hitl.ApprovalGrantScopeProject:
			if strings.TrimSpace(lease.ProjectID) == "" || strings.TrimSpace(lease.ProjectID) != strings.TrimSpace(projectID) {
				continue
			}
		case hitl.ApprovalGrantScopeDevice:
		default:
			continue
		}
		mode := grantedpath.ModeRead
		if lease.GrantedPath.Write {
			mode = grantedpath.ModeWrite
		}
		out = append(out, grantedpath.Grant{
			ID: lease.ID, Path: lease.GrantedPath.Path, Mode: mode,
			Tree: lease.GrantedPath.Tree, ExpiresAt: lease.ExpiresAt,
		})
	}
	return out
}

type untrustedIngestionStore struct {
	store session.Store
}

func (u untrustedIngestionStore) SessionIngestedUntrusted(ctx context.Context, chatSessionID string) bool {
	if u.store == nil {
		return false
	}
	ingested, err := u.store.SessionUntrustedContentResult(ctx, chatSessionID)
	return err == nil && ingested
}

type checkpointConsequenceDeriver struct {
	dests approvals.ConsequenceBandPaths
}

func (d checkpointConsequenceDeriver) ToolApproval(detectionLevel string, secretScreenHit bool) (api.ConsequenceBand, api.ConsequenceCode) {
	bi := approvals.BandInput{
		Kind:            string(api.CheckpointKindToolApproval),
		DetectionLevel:  detectionLevel,
		SecretScreenHit: secretScreenHit,
	}
	if approvals.BandFor(bi, d.dests) != api.ConsequenceBandHighRisk {
		return "", ""
	}
	return api.ConsequenceBandHighRisk, approvals.ConsequenceCode(bi, d.dests)
}

func (d checkpointConsequenceDeriver) WriteRoot(proposedWriteRoot string) (api.ConsequenceBand, api.ConsequenceCode) {
	bi := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		ProposedWriteRoot: proposedWriteRoot,
	}
	if approvals.BandFor(bi, d.dests) != api.ConsequenceBandHighRisk {
		return "", ""
	}
	return api.ConsequenceBandHighRisk, approvals.ConsequenceCode(bi, d.dests)
}
