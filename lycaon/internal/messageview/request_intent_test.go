package messageview

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreserveRequestIntentIgnoresCompactionCheckpoints(t *testing.T) {
	original := api.Message{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Original instruction"}
	checkpoint := api.Message{ID: "checkpoint", Role: api.MessageRoleUser, Origin: api.MessageOriginHost, CompactionCheckpoint: true}
	altered := original
	altered.Content = "Shortened"
	got := PreserveRequestIntent([]api.Message{checkpoint, altered}, []api.Message{original, checkpoint})
	if len(got) != 2 || got[1].Content != original.Content || !got[1].ContextPinned {
		t.Fatalf("instruction lost behind checkpoint: %+v", got)
	}
}
