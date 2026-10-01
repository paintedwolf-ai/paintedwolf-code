package transcript

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func toolResultMessages() []api.Message {
	return []api.Message{
		{
			Role:      api.MessageRoleSystem,
			Content:   "system prompt",
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthoritySystem,
			TrustTier: api.ContentTrustTierTrusted,
		},
		{Role: api.MessageRoleUser, Content: "do the thing"},
		{
			Role:      api.MessageRoleTool,
			Content:   "line one\nline two\nline three",
			Origin:    api.MessageOriginTool,
			Authority: api.ContentAuthorityNone,
			TrustTier: api.ContentTrustTierUntrusted,
		},
	}
}

func TestProjectModelMessagesIsIdempotent(t *testing.T) {
	once := Project(toolResultMessages())
	twice := Project(once)
	thrice := Project(twice)

	if len(once) != len(twice) || len(twice) != len(thrice) {
		t.Fatalf("message count drifted: %d, %d, %d", len(once), len(twice), len(thrice))
	}
	for i := range once {
		if once[i].Content != twice[i].Content {
			t.Fatalf("message %d changed on second projection:\n once: %q\ntwice: %q",
				i, once[i].Content, twice[i].Content)
		}
		if once[i].Content != thrice[i].Content {
			t.Fatalf("message %d changed on third projection:\n  once: %q\nthrice: %q",
				i, once[i].Content, thrice[i].Content)
		}
		if once[i].Role != twice[i].Role {
			t.Fatalf("message %d role changed on reprojection: %q -> %q",
				i, once[i].Role, twice[i].Role)
		}
	}
}

func TestProjectModelMessagesMarksEachDataLineOnce(t *testing.T) {
	out := Project(Project(Project(toolResultMessages())))

	var tool string
	for _, m := range out {
		if m.Role == api.MessageRoleTool {
			tool = m.Content
		}
	}
	if tool == "" {
		t.Fatal("no tool message in projection")
	}
	if strings.Contains(tool, "⟦D⟧⟦D⟧") {
		t.Fatalf("stacked data markers: %q", tool)
	}
	if n := strings.Count(tool, "⟦D:tool⟧"); n != 1 {
		t.Fatalf("⟦D:tool⟧ header count = %d, want 1: %q", n, tool)
	}
	if n := strings.Count(tool, "⟦D⟧"); n != 3 {
		t.Fatalf("⟦D⟧ count = %d, want 3 (one per data line): %q", n, tool)
	}
}

func TestProjectModelMessagesLeavesInstructionsUnmarked(t *testing.T) {
	out := Project(Project(toolResultMessages()))
	for _, m := range out {
		if m.Role != api.MessageRoleUser {
			continue
		}
		if strings.Contains(m.Content, "⟦D") {
			t.Fatalf("user instruction was data-marked: %q", m.Content)
		}
	}
}

func TestProjectModelMessagesAddsNoticeOnce(t *testing.T) {
	out := Project(Project(Project(toolResultMessages())))
	joined := ""
	for _, m := range out {
		joined += m.Content + "\n"
	}
	if n := strings.Count(joined, ContentAuthorityNotice()); n != 1 {
		t.Fatalf("authority notice appears %d times, want 1", n)
	}
}
