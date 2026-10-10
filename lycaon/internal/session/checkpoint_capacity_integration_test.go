//go:build integration

package session

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOverCapTurnMarksTheCheckpointTruncated(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "touch everything")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	for i := 0; i < sessioncheckpoint.MaxPaths+20; i++ {
		rel := "src/f" + strconv.Itoa(i) + ".go"
		testutil.FailErr(t, "seed "+rel,
			os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte("before"), 0o644))
		mgr.Chats.Captures.RecordPrimaryMutation(ctx, sessionID, rel)
	}

	man, err := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store)).Load(t.Context(), sessionID, anchor)
	testutil.FailErr(t, "load manifest", err)
	if len(man.Paths) != sessioncheckpoint.MaxPaths {
		t.Fatalf("captured %d paths, want the cap %d", len(man.Paths), sessioncheckpoint.MaxPaths)
	}
	if !man.Truncated {
		t.Fatal("an over-cap turn must leave a checkpoint that reports itself truncated")
	}
}
