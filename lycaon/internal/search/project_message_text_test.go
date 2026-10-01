package search

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectMessageTextUserAndAssistant(t *testing.T) {
	ts := time.Now().UTC()
	for _, role := range []api.MessageRole{api.MessageRoleUser, api.MessageRoleAssistant} {
		rows := ProjectMessageText("proj-a", "sess-a", api.Message{
			ID: "m1", Role: role, Content: "find the auth bug", CreatedAt: ts,
		})
		if len(rows) != 1 {
			t.Fatalf("role %s: rows = %d, want 1", role, len(rows))
		}
		r := rows[0]
		if r.HitKind != HitKindMessage {
			t.Fatalf("role %s: hit_kind = %q, want message", role, r.HitKind)
		}
		if r.Source != SourceMessage || r.SessionID != "sess-a" || r.MessageID != "m1" {
			t.Fatalf("role %s: row = %+v", role, r)
		}
		if r.SourceRef != "m1" {
			t.Fatalf("role %s: source_ref = %q, want message id", role, r.SourceRef)
		}
		if r.Snippet != "find the auth bug" || r.Role != string(role) {
			t.Fatalf("role %s: snippet/role = %q/%q", role, r.Snippet, r.Role)
		}
	}
}

func TestProjectMessageTextClassifiesDraftByKind(t *testing.T) {
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID:      "m1",
		Role:    api.MessageRoleAssistant,
		Kind:    api.MessageKindDraft,
		Content: "private draft",
	})
	if len(rows) != 1 || rows[0].Role != "draft" {
		t.Fatalf("rows = %+v want role=draft", rows)
	}
}

func TestProjectMessageTextIndexesInternalAssistantAsDraft(t *testing.T) {
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID:         "slot-1",
		Role:       api.MessageRoleAssistant,
		Visibility: api.MessageVisibilityInternal,
		Content:    "private in-progress answer",
	})
	if len(rows) != 1 || rows[0].Role != string(api.MessageKindDraft) {
		t.Fatalf("rows = %+v want one draft row", rows)
	}
}

func TestProjectMessageTextClassifiesAcceptedCloseoutAsAssistant(t *testing.T) {
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID:          "m1",
		Role:        api.MessageRoleAssistant,
		Kind:        api.MessageKindDraft,
		DraftStatus: api.DraftStatusCommitted,
		Content:     "accepted answer",
	})
	if len(rows) != 1 || rows[0].Role != "assistant" {
		t.Fatalf("rows = %+v want role=assistant", rows)
	}
}

func TestProjectMessageTextSkipsNonConversational(t *testing.T) {
	ts := time.Now().UTC()
	cases := []struct {
		name string
		msg  api.Message
	}{
		{"tool role", api.Message{ID: "t1", Role: api.MessageRoleTool, Content: "result"}},
		{"system role", api.Message{ID: "s1", Role: api.MessageRoleSystem, Content: "sys"}},
		{"empty content", api.Message{ID: "e1", Role: api.MessageRoleAssistant, Content: "   "}},
		{"internal visibility", api.Message{ID: "i1", Role: api.MessageRoleUser, Content: "kick", Visibility: api.MessageVisibilityInternal}},
		{"workflow boundary", api.Message{ID: "b1", Role: api.MessageRoleUser, Content: "x", Kind: api.MessageKindWorkflowBoundary}},
		{"progress update", api.Message{ID: "p1", Role: api.MessageRoleAssistant, Content: "x", Kind: api.MessageKindProgressUpdate}},
	}
	for _, tc := range cases {
		tc.msg.CreatedAt = ts
		if rows := ProjectMessageText("proj-a", "sess-a", tc.msg); len(rows) != 0 {
			t.Fatalf("%s: expected no rows, got %d", tc.name, len(rows))
		}
	}
}

func TestProjectMessageTextStripsInjectSentinel(t *testing.T) {
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID:        "m1",
		Role:      api.MessageRoleUser,
		Content:   "<!-- lycaon-worker-task-preamble:v1 -->\n\nrewrite the greeting helper",
		CreatedAt: time.Now().UTC(),
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Snippet != "rewrite the greeting helper" {
		t.Fatalf("snippet = %q, want the assignment text alone", rows[0].Snippet)
	}
}

func TestProjectMessageTextSkipsSentinelOnlyContent(t *testing.T) {
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID:        "m1",
		Role:      api.MessageRoleUser,
		Content:   "<!-- lycaon-worker-leg:v1 -->",
		CreatedAt: time.Now().UTC(),
	})
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 for scaffolding-only content", len(rows))
	}
}

func TestProjectMessageTextKeepsProseHTMLComments(t *testing.T) {
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID:        "m1",
		Role:      api.MessageRoleAssistant,
		Content:   "<!-- TODO: revisit this -->\nthe parser drops trailing commas",
		CreatedAt: time.Now().UTC(),
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Snippet, "TODO: revisit this") {
		t.Fatalf("snippet = %q, want the non-versioned comment kept", rows[0].Snippet)
	}
}

func TestProjectMessageTextCapsSnippet(t *testing.T) {
	long := make([]rune, messageSnippetMaxRunes+50)
	for i := range long {
		long[i] = 'a'
	}
	rows := ProjectMessageText("proj-a", "sess-a", api.Message{
		ID: "m1", Role: api.MessageRoleAssistant, Content: string(long), CreatedAt: time.Now().UTC(),
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0].Truncated {
		t.Fatal("expected Truncated=true for over-cap content")
	}
	if n := len([]rune(rows[0].Snippet)); n > messageSnippetMaxRunes {
		t.Fatalf("snippet runes = %d, want <= %d", n, messageSnippetMaxRunes)
	}
}
