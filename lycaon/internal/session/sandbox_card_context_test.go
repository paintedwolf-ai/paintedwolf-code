package session

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settings/settingstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

type cardContextProvenance struct {
	fixedProvenance
	t     *testing.T
	want  context.Context
	calls int
}

func (p *cardContextProvenance) IsSessionOwned(ctx context.Context, _, _ string, _ uint16) (bool, ProvenanceEvidence) {
	p.t.Helper()
	if ctx != p.want {
		p.t.Error("approval card replaced the invoking operation context")
	}
	p.calls++
	return false, ProvenanceEvidence{}
}

func TestApprovalCardsKeepInvokingContextForHolderObservations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := &cardContextProvenance{t: t, want: ctx}
	_, err := (&ListenCheckpointBroker{Provenance: p}).buildListenCard(ctx,
		tools.LocalListenAsk{ProjectDir: t.TempDir()}, "child", "root", []uint16{8000})
	testutil.FailErr(t, "build listen card", err)
	_, err = (&LoopbackCheckpointBroker{Provenance: p}).buildCard(ctx,
		tools.LoopbackConnectAsk{ProjectDir: t.TempDir()}, "child", "root", []uint16{8001})
	testutil.FailErr(t, "build loopback card", err)
	_, err = (&LocalNetworkCheckpointBroker{Provenance: p}).buildCard(ctx,
		tools.LocalNetworkAsk{ProjectDir: t.TempDir(), ListenPorts: []uint16{8002}, ConnectPorts: []uint16{8003}}, "child", "root")
	testutil.FailErr(t, "build local network card", err)
	if p.calls != 4 {
		t.Fatalf("observed %d port holders, want all four", p.calls)
	}
}

type attributionRuleSource struct{ rules []settings.ApprovalRule }

func (s attributionRuleSource) RuleLayers(context.Context, string) settings.ApprovalRuleLayers {
	return settings.ApprovalRuleLayers{Device: s.rules}
}

type attributionCheckpoints struct {
	unusedWriteRootCheckpoints
	suppressed bool
	status     hitl.DecisionStatus
	resolvedBy string
	reason     string
	requested  int
}

func (c *attributionCheckpoints) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return c.suppressed, nil
}

func (c *attributionCheckpoints) RequestCheckpoint(_ context.Context, _ hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	c.requested++
	return &hitl.CheckpointResponse{CheckpointID: "cp-attribution", Status: hitl.DecisionStatusPending}, nil
}

func (c *attributionCheckpoints) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	res := &hitl.DecisionResult{
		Approved: c.status == hitl.DecisionStatusApproved,
		Comments: c.reason,
	}
	return &hitl.CheckpointResponse{
		CheckpointID: "cp-attribution",
		Status:       c.status,
		ResolvedBy:   c.resolvedBy,
		Result:       res,
	}, nil
}

func TestWriteRootAndReadPathDenialCauseAttribution(t *testing.T) {
	t.Run("approval_rule_denied", assertBrokerRuleDenialAttribution)
	t.Run("control_plane_policy_denied", assertBrokerControlPlaneDenialAttribution)
	t.Run("human_nonhuman_prior_and_expired_attribution", assertBrokerHumanAndExpiredAttribution)
}

func assertBrokerRuleDenialAttribution(t *testing.T) {
	denyRoot := filepath.Join(t.TempDir(), "blocked-write")
	denyWriteRule := settings.ApprovalRule{
		Category: settings.ApprovalCategoryWriteRoot,
		Pattern:  denyRoot,
		Effect:   settings.ApprovalEffectDeny,
		Source: settings.ApprovalRuleSource{
			Scope:  settings.ApprovalRuleScopeDevice,
			PackID: "acme/policy",
			UnitID: "approvals/rules/deny-write-root",
		},
	}

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	readRule := settings.ApprovalRule{
		Category: settings.ApprovalCategoryPath,
		Pattern:  "*.pem",
		Effect:   settings.ApprovalEffectDeny,
		Source: settings.ApprovalRuleSource{
			Scope:  settings.ApprovalRuleScopeDevice,
			PackID: "acme/policy",
			UnitID: "approvals/rules/deny-pem-read",
		},
	}
	sources := settingstest.EmptySources()
	sources.ApprovalRules = attributionRuleSource{rules: []settings.ApprovalRule{readRule}}
	authority := settings.NewRuleApprovalGate(store, sources)

	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
		Authority:   authority,
		Posture:     func(string) gate.Posture { return gate.PostureBalanced },
		Rule: func(_ context.Context, _, _, proposed string) (settings.ApprovalRule, bool) {
			if proposed == denyRoot {
				return denyWriteRule, true
			}
			return settings.ApprovalRule{}, false
		},
	}

	wRes, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess-rule", ProposedWriteRoot: denyRoot, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize rule-denied write root", err)
	if wRes.Authorized || wRes.Raised || !wRes.Denied || wRes.DenialCause != isolation.PathDenialApprovalRule || len(wRes.DeniedRules) != 1 {
		t.Fatalf("write-root rule denial = %+v", wRes)
	}
	if wRes.DeniedRules[0].UnitID != denyWriteRule.Source.UnitID || wRes.DeniedRules[0].PackID != denyWriteRule.Source.PackID {
		t.Fatalf("write-root DeniedRules lost source metadata: %+v", wRes.DeniedRules)
	}

	pemDir := t.TempDir()
	pemPath := filepath.Join(pemDir, "cert.pem")
	rRes, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess-rule", ProposedReadPath: pemPath, ReadDenyPaths: []string{pemDir}, ToolName: "command",
	})
	testutil.FailErr(t, "AuthorizeRead rule-denied path", err)
	if rRes.Authorized || rRes.Raised || !rRes.Denied || rRes.DenialCause != isolation.PathDenialApprovalRule || len(rRes.DeniedRules) == 0 {
		t.Fatalf("read-path rule denial = %+v", rRes)
	}
	if rRes.DeniedRules[0].UnitID != readRule.Source.UnitID || rRes.DeniedRules[0].Pattern != "*.pem" {
		t.Fatalf("read-path DeniedRules lost source metadata: %+v", rRes.DeniedRules)
	}
}

func assertBrokerControlPlaneDenialAttribution(t *testing.T) {
	ancestor := fspath.CanonicalPath(t.TempDir())
	cfg := filepath.Join(ancestor, "lycaon-state")
	t.Setenv("TMPDIR", ancestor)
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	broker := &WriteRootCheckpointBroker{
		Checkpoints: unusedWriteRootCheckpoints{t: t},
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
	}

	for _, root := range []string{filepath.Join(cfg, "approvals.yaml"), ancestor} {
		wRes, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
			SessionID: "sess-cp", ProposedWriteRoot: root, ToolName: "command",
		})
		testutil.FailErr(t, "Authorize control-plane write", err)
		if wRes.Authorized || wRes.Raised || !wRes.Denied || wRes.DenialCause != isolation.PathDenialControlPlanePolicy {
			t.Fatalf("root %s write control-plane result = %+v", root, wRes)
		}
	}

	settled := broker.ReviewSharedTemp(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess-cp", ToolName: "command",
	}, []string{ancestor}, true)
	if !slices.Equal(settled.Declined, []string{ancestor}) || len(settled.Unanswered) != 0 || len(settled.Allowed) != 0 {
		t.Fatalf("control-plane shared-temp settlement = %+v, want Declined=[%s] and empty Unanswered/Allowed", settled, ancestor)
	}

	rRes, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess-cp", ProposedReadPath: filepath.Join(cfg, "approvals.yaml"), ToolName: "command",
	})
	testutil.FailErr(t, "AuthorizeRead control-plane read", err)
	if rRes.Authorized || rRes.Raised || !rRes.Denied || rRes.DenialCause != isolation.PathDenialControlPlanePolicy {
		t.Fatalf("read control-plane result = %+v", rRes)
	}
}

func assertBrokerHumanAndExpiredAttribution(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "isolated-cfg"))
	outside := filepath.Join(t.TempDir(), "external-target")
	protectedReadDir := t.TempDir()
	protectedRead := filepath.Join(protectedReadDir, "secret.json")

	broker := &WriteRootCheckpointBroker{
		Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
		Posture:     func(string) gate.Posture { return gate.PostureBalanced },
	}

	broker.Checkpoints = &attributionCheckpoints{
		status:     hitl.DecisionStatusRejected,
		resolvedBy: authzledger.ResolvedByHuman,
		reason:     "Use the attached workspace instead.",
	}
	wDeclined, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess-human", ToolCallID: "tc-1", ProposedWriteRoot: outside, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize user decline", err)
	if !wDeclined.Raised || wDeclined.Authorized || !wDeclined.Denied ||
		wDeclined.DenialCause != isolation.PathDenialUserDeclined ||
		wDeclined.UserGuidance != "Use the attached workspace instead." {
		t.Fatalf("user-declined write root = %+v", wDeclined)
	}

	rDeclined, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess-human", ToolCallID: "tc-2", ProposedReadPath: protectedRead,
		ReadDenyPaths: []string{protectedReadDir}, ToolName: "command",
	})
	testutil.FailErr(t, "AuthorizeRead user decline", err)
	if !rDeclined.Raised || rDeclined.Authorized || !rDeclined.Denied ||
		rDeclined.DenialCause != isolation.PathDenialUserDeclined ||
		rDeclined.UserGuidance != "Use the attached workspace instead." {
		t.Fatalf("user-declined read path = %+v", rDeclined)
	}

	for _, actor := range []string{"", authzledger.ResolvedBySystemDeny} {
		broker.Checkpoints = &attributionCheckpoints{
			status:     hitl.DecisionStatusRejected,
			resolvedBy: actor,
			reason:     "system note must not masquerade as user guidance",
		}
		wNonHuman, wErr := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
			SessionID: "sess-nh-" + actor, ToolCallID: "tc-nh-w", ProposedWriteRoot: outside, ToolName: "command",
		})
		testutil.FailErr(t, "Authorize nonhuman reject", wErr)
		if !wNonHuman.Raised || wNonHuman.Authorized || !wNonHuman.Denied ||
			wNonHuman.DenialCause != isolation.PathDenialNonHumanRejected || wNonHuman.UserGuidance != "" {
			t.Fatalf("actor=%q write-root nonhuman reject = %+v", actor, wNonHuman)
		}
		rNonHuman, rErr := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
			SessionID: "sess-nh-" + actor, ToolCallID: "tc-nh-r", ProposedReadPath: protectedRead,
			ReadDenyPaths: []string{protectedReadDir}, ToolName: "command",
		})
		testutil.FailErr(t, "AuthorizeRead nonhuman reject", rErr)
		if !rNonHuman.Raised || rNonHuman.Authorized || !rNonHuman.Denied ||
			rNonHuman.DenialCause != isolation.PathDenialNonHumanRejected || rNonHuman.UserGuidance != "" {
			t.Fatalf("actor=%q read-path nonhuman reject = %+v", actor, rNonHuman)
		}
	}

	cpPrior := &attributionCheckpoints{suppressed: true}
	broker.Checkpoints = cpPrior
	wPrior, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess-prior", ToolCallID: "tc-3", ProposedWriteRoot: outside, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize suppressed repeat", err)
	if cpPrior.requested != 0 || !wPrior.Raised || !wPrior.Denied || wPrior.DenialCause != isolation.PathDenialPriorUserDeclined {
		t.Fatalf("prior-declined write root = %+v (cards=%d)", wPrior, cpPrior.requested)
	}

	rPrior, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess-prior", ToolCallID: "tc-4", ProposedReadPath: protectedRead,
		ReadDenyPaths: []string{protectedReadDir}, ToolName: "command",
	})
	testutil.FailErr(t, "AuthorizeRead suppressed repeat", err)
	if cpPrior.requested != 0 || !rPrior.Raised || !rPrior.Denied || rPrior.DenialCause != isolation.PathDenialPriorUserDeclined {
		t.Fatalf("prior-declined read path = %+v (cards=%d)", rPrior, cpPrior.requested)
	}

	broker.Checkpoints = &attributionCheckpoints{status: hitl.DecisionStatusExpired}
	wExpired, err := broker.Authorize(t.Context(), command.SandboxWriteRootAsk{
		SessionID: "sess-expired", ToolCallID: "tc-5", ProposedWriteRoot: outside, ToolName: "command",
	})
	testutil.FailErr(t, "Authorize expired checkpoint", err)
	if wExpired.Authorized || wExpired.Denied || wExpired.DenialCause != isolation.PathDenialNone {
		t.Fatalf("expired write root = %+v", wExpired)
	}

	rExpired, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{
		SessionID: "sess-expired", ToolCallID: "tc-6", ProposedReadPath: protectedRead,
		ReadDenyPaths: []string{protectedReadDir}, ToolName: "command",
	})
	testutil.FailErr(t, "AuthorizeRead expired checkpoint", err)
	if rExpired.Authorized || rExpired.Denied || rExpired.DenialCause != isolation.PathDenialNone {
		t.Fatalf("expired read path = %+v", rExpired)
	}
}
