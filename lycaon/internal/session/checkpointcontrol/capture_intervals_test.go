package checkpointcontrol

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCaptureIntervalsRetainTheFirstPreimage(t *testing.T) {
	rewinds, repository, _, sessionID, dir := newRewindControlFixture(t)
	owner, err := repository.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	ctx := people.WithCaller(t.Context(), owner)
	anchor := api.Message{ID: "capture-anchor", Role: api.MessageRoleUser, Content: "edit file", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted}
	testutil.FailErr(t, "append anchor", repository.AppendMessages(ctx, sessionID, anchor))
	file := filepath.Join(dir, "item.txt")
	testutil.FailErr(t, "seed preimage", os.WriteFile(file, []byte("before"), 0o640))
	testutil.FailErr(t, "open interval", rewinds.captures.SealPromptCheckpoint(ctx, sessionID, anchor.ID))
	rewinds.captures.RecordPrimaryMutation(ctx, sessionID, "item.txt")
	checkpoint, err := rewinds.captures.ForSession(ctx, sessionID)
	testutil.FailErr(t, "resolve checkpoint", err)
	original, err := checkpoint.Load(ctx, sessionID, anchor.ID)
	testutil.FailErr(t, "load preimage", err)
	testutil.FailErr(t, "write after", os.WriteFile(file, []byte("after"), 0o640))
	rewinds.captures.RecordPrimaryMutation(ctx, sessionID, "item.txt")
	updated, err := checkpoint.Load(ctx, sessionID, anchor.ID)
	testutil.FailErr(t, "load repeated touch", err)
	if original.Paths["item.txt"].SHA256 == "" || updated.Paths["item.txt"].SHA256 != original.Paths["item.txt"].SHA256 {
		t.Fatalf("first-touch preimage changed: before=%+v after=%+v", original.Paths, updated.Paths)
	}
}
