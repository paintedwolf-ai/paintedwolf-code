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

func TestCapturedPromotionAndBlueprintRemainInCheckpoint(t *testing.T) {
	rewinds, repository, _, sessionID, dir := newRewindControlFixture(t)
	ctx := t.Context()
	for _, name := range []string{"one.txt", "two.txt"} {
		testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	}
	testutil.FailErr(t, "open interval", rewinds.captures.SealPromptCheckpoint(ctx, sessionID, "promotion"))
	rewinds.captures.RecordPromotedPrimaryPaths(ctx, sessionID, []string{"one.txt", "two.txt"})
	rewinds.captures.RecordBlueprintBinding(ctx, sessionID, "plans/original.md")
	rewinds.captures.RecordBlueprintBinding(ctx, sessionID, "plans/replacement.md")
	checkpoints, err := rewinds.captures.ForSession(ctx, sessionID)
	testutil.FailErr(t, "resolve checkpoint", err)
	manifest, err := checkpoints.Load(ctx, sessionID, "promotion")
	testutil.FailErr(t, "load checkpoint", err)
	if manifest.BlueprintPath != "plans/original.md" || len(manifest.Paths) != 2 {
		t.Fatalf("retained checkpoint=%+v", manifest)
	}
	_, err = checkpoints.Open(ctx, "deleted-session", "orphan")
	testutil.FailErr(t, "seed orphan", err)
	if n := rewinds.captures.RemoveOrphanCheckpoints(ctx, dir); n != 1 {
		t.Fatalf("removed %d orphan checkpoint trees", n)
	}
	if _, err = checkpoints.Load(ctx, sessionID, "promotion"); err != nil {
		t.Fatalf("live checkpoint removed: %v", err)
	}
	ids, err := repository.ExistingSessionIDs(ctx, []string{sessionID})
	testutil.FailErr(t, "live session", err)
	if !ids[sessionID] {
		t.Fatal("checkpoint cleanup removed live session")
	}
}
