package hitl_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingInstaller keeps every option it was asked to install.
type recordingInstaller struct {
	mu        sync.Mutex
	installed []hitl.ApprovalOption
}

func (r *recordingInstaller) InstallApprovalOption(_ context.Context, _ string, option hitl.ApprovalOption) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installed = append(r.installed, option)
	return func() {}, nil
}

func (r *recordingInstaller) grantIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, option := range r.installed {
		for _, delta := range option.Authority {
			if _, id, ok := delta.ChatLifetime(); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

func TestChatLifetimeCoversOnlyChatScopedAuthority(t *testing.T) {
	chatGrant := &hitl.ApprovalGrant{ID: "grant_chat", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "chat-1"}
	projectGrant := &hitl.ApprovalGrant{ID: "grant_project", Scope: hitl.ApprovalGrantScopeProject, ProjectID: "p"}
	cases := []struct {
		name  string
		delta hitl.ApprovalAuthorityDelta
		want  string
	}{
		{"chat generic grant", hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityGenericGrant, Grant: chatGrant}, "grant_chat"},
		{"project generic grant", hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityGenericGrant, Grant: projectGrant}, ""},
		{"write root", hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityWriteRootChat, Grant: &hitl.ApprovalGrant{ID: "grant_root"}, ChatSessionID: "chat-1"}, "grant_root"},
		{"quiet", hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityAskQuiet, ChatSessionID: "chat-1", AskQuiet: &hitl.AskQuietDelta{ID: "quiet_1"}}, "quiet_1"},
		{"current action", hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityCurrentAction}, ""},
		{"chat grant without a chat", hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityGenericGrant, Grant: &hitl.ApprovalGrant{ID: "grant_orphan", Scope: hitl.ApprovalGrantScopeChat}}, ""},
	}
	for _, tc := range cases {
		chat, id, ok := tc.delta.ChatLifetime()
		if tc.want == "" {
			if ok {
				t.Errorf("%s: recorded as chat grant %q in %q", tc.name, id, chat)
			}
			continue
		}
		if !ok || id != tc.want || chat != "chat-1" {
			t.Errorf("%s: chat lifetime = (%q, %q, %v), want (chat-1, %s)", tc.name, chat, id, ok, tc.want)
		}
	}
}

func TestChatGrantSurvivesRestartAndRevoke(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	action := hitl.ProposedAction{Tool: "command", SessionID: sessionID, Args: map[string]any{"command": "make"}}
	chat := hitl.ApprovalGrant{ElevatedEffects: []api.ElevatedAccessEffect{api.ElevatedAccessEffectHostExecution}, ID: "grant_make", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, Title: hitl.TitleAllowForThisChat}
	day := hitl.ApprovalGrant{ID: "grant_make_day", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, Title: hitl.TitleAllowFor1Day, TTLSeconds: hitl.DayRungTTLSeconds}
	presentation, reasons := approvalPlanPresentation()
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run make",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "make"}},
	}, presentation, reasons, []hitl.ApprovalOption{
		{
			ID: "chat", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat, Rung: hitl.ApprovalRungChat,
			Title: hitl.TitleAllowForThisChat, Coverage: "make", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "the command changes",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &chat}},
		},
		{
			ID: "day", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat, Rung: hitl.ApprovalRungDay,
			Title: hitl.TitleAllowFor1Day, Coverage: "make", ExpiresWhen: hitl.ExpiresIn1DayOrChatDeleted, ReaskWhen: "the command changes",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &day}},
		},
	}, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	if plan.RecommendedOptionID != "chat" {
		t.Fatalf("face = %q, want the chat rung", plan.RecommendedOptionID)
	}
	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ProposedAction: &action, ApprovalPlan: plan,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	owner := hostOwner(t, sqlDB)
	_, err = mgr.Authority.ResolveApprovalOption(people.WithCaller(ctx, owner), sessionID, resp.CheckpointID, "chat")
	testutil.FailErr(t, "ResolveApprovalOption", err)

	restarted := &recordingInstaller{}
	mgr.Authority.SetApprovalAuthorityInstaller(restarted)
	testutil.FailErr(t, "RestoreChatGrants", mgr.Authority.RestoreChatGrants(ctx))
	if got := restarted.grantIDs(); len(got) != 1 || got[0] != chat.ID {
		t.Fatalf("restored grants = %v, want [%s]", got, chat.ID)
	}
	if grant := restarted.installed[0].Authority[0].Grant; grant == nil || len(grant.ElevatedEffects) != 1 || grant.ElevatedEffects[0] != api.ElevatedAccessEffectHostExecution {
		t.Fatalf("restored elevated descriptor = %+v", grant)
	}
	if grant := restarted.installed[0].Authority[0].Grant; grant == nil || grant.GrantedByPersonID != owner.ID {
		t.Fatalf("restored grant = %+v, want it granted by %s", grant, owner.ID)
	}

	revoked, err := mgr.Authority.ForgetChatGrant(ctx, chat.ID)
	testutil.FailErr(t, "ForgetChatGrant", err)
	if !revoked {
		t.Fatal("the recorded chat grant was not found to revoke")
	}
	again := &recordingInstaller{}
	mgr.Authority.SetApprovalAuthorityInstaller(again)
	testutil.FailErr(t, "RestoreChatGrants after revoke", mgr.Authority.RestoreChatGrants(ctx))
	if got := again.grantIDs(); len(got) != 0 {
		t.Fatalf("revoked grant came back after restart: %v", got)
	}
}

func TestDayRungKeepsItsDeadlineAcrossRestart(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	action := hitl.ProposedAction{Tool: "command", SessionID: sessionID, Args: map[string]any{"command": "make"}}
	day := hitl.ApprovalGrant{ID: "grant_make_day", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, Title: hitl.TitleAllowFor1Day, TTLSeconds: hitl.DayRungTTLSeconds}
	presentation, reasons := approvalPlanPresentation()
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run make",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "make"}},
	}, presentation, reasons, []hitl.ApprovalOption{{
		ID: "day", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat, Rung: hitl.ApprovalRungDay,
		Title: hitl.TitleAllowFor1Day, Coverage: "make", ExpiresWhen: hitl.ExpiresIn1DayOrChatDeleted, ReaskWhen: "the command changes",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &day}},
	}}, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ProposedAction: &action, ApprovalPlan: plan,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	approvedAt := time.Now()
	_, err = mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "day")
	testutil.FailErr(t, "ResolveApprovalOption", err)

	restarted := &recordingInstaller{}
	mgr.Authority.SetApprovalAuthorityInstaller(restarted)
	testutil.FailErr(t, "RestoreChatGrants", mgr.Authority.RestoreChatGrants(ctx))
	if len(restarted.installed) != 1 {
		t.Fatalf("restored %d options, want 1", len(restarted.installed))
	}
	restored := restarted.installed[0].Authority[0].Grant
	remaining := time.Duration(restored.TTLSeconds) * time.Second
	if remaining <= 0 || remaining > time.Until(approvedAt.Add(hitl.DayRungTTLSeconds*time.Second))+2*time.Second {
		t.Fatalf("restored day grant has %s left, want the remainder of its original day", remaining)
	}
}
