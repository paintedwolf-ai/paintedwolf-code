package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

func testPolicyMsg(rootID, path, content string) api.Message {
	return api.Message{
		ID:        "host-agent-policy-test/" + rootID + "/" + path,
		Role:      api.MessageRoleSystem,
		Content:   content,
		Origin:    api.MessageOriginProject,
		Authority: api.ContentAuthorityDeveloper,
		TrustTier: api.ContentTrustTierTrusted,
		SourceContext: &api.SourceContext{
			Locations: []api.NavigationTarget{{
				ProjectID: "proj",
				RootID:    rootID,
				Path:      path,
				EntryKind: api.NavigationEntryKindFile,
			}},
		},
	}
}

func TestSplitPreHistoryAgentsMDPolicies(t *testing.T) {
	sess := &api.Session{ID: "s1", WorkspaceRootID: "r1"}

	t.Run("root and overlay without nested lift together", func(t *testing.T) {
		rootMsg := testPolicyMsg("r1", "AGENTS.md", "root")
		overlayMsg := testPolicyMsg("r1", ".paintedwolf/AGENTS.md", "overlay")
		pre, tail := splitPreHistoryAgentsMDPolicies(sess, AgentsMDPolicyDelivery{
			Messages:     []api.Message{rootMsg, overlayMsg},
			ActiveRootID: "r1",
		})
		if len(pre) != 2 || pre[0].ID != rootMsg.ID || pre[1].ID != overlayMsg.ID {
			t.Fatalf("pre = %+v, want [root overlay]", pre)
		}
		if len(tail) != 0 {
			t.Fatalf("tail = %+v, want empty", tail)
		}
	})

	t.Run("nested policy keeps overlay in tail after nested", func(t *testing.T) {
		rootMsg := testPolicyMsg("r1", "AGENTS.md", "root")
		nestedMsg := testPolicyMsg("r1", "sub/AGENTS.md", "nested")
		overlayMsg := testPolicyMsg("r1", ".paintedwolf/AGENTS.md", "overlay")
		pre, tail := splitPreHistoryAgentsMDPolicies(sess, AgentsMDPolicyDelivery{
			Messages:     []api.Message{rootMsg, nestedMsg, overlayMsg},
			ActiveRootID: "r1",
		})
		if len(pre) != 1 || pre[0].ID != rootMsg.ID {
			t.Fatalf("pre = %+v, want [root]", pre)
		}
		if len(tail) != 2 || tail[0].ID != nestedMsg.ID || tail[1].ID != overlayMsg.ID {
			t.Fatalf("tail = %+v, want [nested overlay]", tail)
		}
	})

	t.Run("multi-root lifts only active root and keeps diagnostic in tail", func(t *testing.T) {
		r1Root := testPolicyMsg("r1", "AGENTS.md", "r1 root")
		r2Root := testPolicyMsg("r2", "AGENTS.md", "r2 root")
		diag := api.Message{
			Role:      api.MessageRoleSystem,
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthoritySystem,
			TrustTier: api.ContentTrustTierTrusted,
			Content:   "unavailable",
		}
		pre, tail := splitPreHistoryAgentsMDPolicies(sess, AgentsMDPolicyDelivery{
			Messages:     []api.Message{r1Root, r2Root, diag},
			ActiveRootID: "r1",
		})
		if len(pre) != 1 || pre[0].ID != r1Root.ID {
			t.Fatalf("pre = %+v, want [r1 root]", pre)
		}
		if len(tail) != 2 || tail[0].ID != r2Root.ID || tail[1].Content != "unavailable" {
			t.Fatalf("tail = %+v, want [r2 root, diag]", tail)
		}
	})
}

func TestBuildCompletionMessagesPlacesRootPolicyBeforeHistoryAndAfterStanding(t *testing.T) {
	eng := prefixStabilityEngine(t)
	deps := eng.deps()
	deps.AgentsMD.Index = func(context.Context, *api.Session) (api.Message, bool) {
		return api.Message{
			Role:      api.MessageRoleSystem,
			Content:   "Available AGENTS.md files:\n- AGENTS.md",
			Origin:    api.MessageOriginProject,
			Authority: api.ContentAuthorityDeveloper,
			TrustTier: api.ContentTrustTierTrusted,
		}, true
	}
	rootPolicy := testPolicyMsg("r1", "AGENTS.md", "Root project policy: always run ./task.")
	nestedPolicy := testPolicyMsg("r1", "sub/AGENTS.md", "Nested policy: check sub/.")
	overlayPolicy := testPolicyMsg("r1", ".paintedwolf/AGENTS.md", "Overlay policy: override.")
	deps.AgentsMD.Policies = func(context.Context, *api.Session, []api.Message) (AgentsMDPolicyDelivery, error) {
		return AgentsMDPolicyDelivery{
			Messages:     []api.Message{rootPolicy, nestedPolicy, overlayPolicy},
			ActiveRootID: "r1",
		}, nil
	}
	eng.SetDeps(deps)

	sess := &api.Session{
		ID:              "pre-history-policy",
		Posture:         api.SessionPostureBuild,
		AgentType:       orchestration.ProfileCoordinator,
		WorkspaceRootID: "r1",
		WorkspacePath:   t.TempDir(),
	}
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "edit sub/file.go"},
	}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(t.Context(), sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}

	standingIdx, rootIdx, historyIdx, nestedIdx, overlayIdx := -1, -1, -1, -1, -1
	for i, m := range msgs {
		if m.PromptCacheBreakpoint == api.PromptCacheTierStanding {
			standingIdx = i
		}
		if m.PromptCacheBreakpoint == api.PromptCacheTierHistory {
			historyIdx = i
		}
		switch m.ID {
		case rootPolicy.ID:
			rootIdx = i
		case nestedPolicy.ID:
			nestedIdx = i
		case overlayPolicy.ID:
			overlayIdx = i
		}
		if m.ID == rootPolicy.ID && strings.Contains(m.Content, "Data from project") {
			t.Fatalf("project policy should keep developer authority without data header: %q", m.Content)
		}
	}
	if standingIdx < 0 || rootIdx < 0 || historyIdx < 0 || nestedIdx < 0 || overlayIdx < 0 {
		t.Fatalf("missing expected indices: standing=%d root=%d history=%d nested=%d overlay=%d", standingIdx, rootIdx, historyIdx, nestedIdx, overlayIdx)
	}
	if !(standingIdx < rootIdx && rootIdx < historyIdx && historyIdx < nestedIdx && nestedIdx < overlayIdx) {
		t.Fatalf("unexpected ordering: standing=%d root=%d history=%d nested=%d overlay=%d", standingIdx, rootIdx, historyIdx, nestedIdx, overlayIdx)
	}
}
