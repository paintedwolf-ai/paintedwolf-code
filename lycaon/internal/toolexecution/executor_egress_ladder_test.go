package toolexecution

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

// A widening posture adds the command's network right after the host's chat
// permission and keeps every other rung where it was, so the face, the first
// primary chat rung, stays the host.
func TestCommandNetworkSitsBesideTheHostsChatPermission(t *testing.T) {
	once := hitl.ApprovalGrantOffer{ID: "once", Rung: hitl.ApprovalRungOnce}
	host := hitl.ApprovalGrantOffer{ID: "host", Rung: hitl.ApprovalRungChat}
	project := hitl.ApprovalGrantOffer{ID: "project", Rung: hitl.ApprovalRungProject}
	menu := hitl.ApprovalGrantOffer{ID: "menu", Rung: hitl.ApprovalRungChat, Group: hitl.GroupAlsoAllow}
	command := hitl.ApprovalGrantOffer{ID: "command", Rung: hitl.ApprovalRungChat}

	ids := func(offers []hitl.ApprovalGrantOffer) []string {
		out := make([]string, 0, len(offers))
		for _, offer := range offers {
			out = append(out, offer.ID)
		}
		return out
	}
	if got := ids(besideChatSlot([]hitl.ApprovalGrantOffer{once, menu, host, project}, command)); !slices.Equal(got, []string{"once", "menu", "host", "command", "project"}) {
		t.Fatalf("ladder = %v", got)
	}
	if got := ids(besideChatSlot([]hitl.ApprovalGrantOffer{once, project}, command)); !slices.Equal(got, []string{"once", "project", "command"}) {
		t.Fatalf("ladder without a chat slot = %v", got)
	}
}
