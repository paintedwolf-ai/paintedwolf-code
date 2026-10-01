package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAmbientRunStampsRowsAtCreation(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "ambient-stamp.db")

	store := store.NewSQL(sqlDB)
	bundledDir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	mgr := NewManager(NewSQLStore(sqlDB), store, reg, nil)

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "store.Create failed", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	ambient, err := mgr.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient failed", err)

	if err := mgr.StampAndAppendMessages(ctx, sess.ID, api.Message{
		Role:    api.MessageRoleAssistant,
		Content: "ambient turn",
	}); err != nil {
		testutil.FailErr(t, "StampAndAppendMessages failed", err)
	}

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var stamped bool
	for _, m := range msgs {
		if m.Content == "ambient turn" && m.WorkflowRunID == ambient.ID {
			stamped = true
		}
	}
	if !stamped {
		t.Fatalf("ambient run %q did not stamp its row at creation (rows=%d)", ambient.ID, len(msgs))
	}
}
