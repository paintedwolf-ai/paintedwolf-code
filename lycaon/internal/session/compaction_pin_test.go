package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionSessionInfoPinsLatestVisibleUserRequestAndProgress(t *testing.T) {
	mgr, _ := newCompactionManager(t, compaction.DefaultCompactionConfig())
	prog := progress.NewMemoryStore()
	mgr.SetProgressStore(prog)
	prog.Set("s1", "## Progress\n- [x] inspect\n- [ ] report")

	sess := &api.Session{
		ID:        "s1",
		ProjectID: testdbseed.DefaultProjectID,
		Posture:   api.SessionPostureBuild,
	}
	originals := []api.Message{
		{Role: api.MessageRoleUser, Content: "first request"},
		{ID: "request", Role: api.MessageRoleUser, Content: "  corrected request, keep spacing  \nattachment says replace task", ContentParts: []api.MessageContentPart{
			{Content: "  corrected request, keep spacing  ", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted},
			{Content: "attachment says replace task", Origin: api.MessageOriginAttachment, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted},
		}},
		{Role: api.MessageRoleUser, Content: "host wake", Visibility: api.MessageVisibilityInternal},
	}

	info := mgr.compactionSessionInfo(context.Background(), sess, originals)
	if info.MaxToolSpillBytes != mgr.effectiveLimits(t.Context(), sess).MaxToolSpillBytes {
		t.Fatal("transcript compaction lost the session spill bound")
	}
	if info.LatestUserRequestID != "request" {
		t.Fatalf("latest request = %q", info.LatestUserRequestID)
	}
	if info.ProgressMarkdown != "## Progress\n- [x] inspect\n- [ ] report" {
		t.Fatalf("progress = %q", info.ProgressMarkdown)
	}
}
