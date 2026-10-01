package checkpoint

import (
	"context"
	"errors"
	"testing"

	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointOperationsHonorCancellation(t *testing.T) {
	database := testdbfixture.Open(t, "checkpoint.db")
	root := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, root)
	repository := sessionstore.NewSQL(database)
	sess, err := repository.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create checkpoint session", err)
	checkpoints := New(t.TempDir(), root, repository)
	_, err = checkpoints.Open(t.Context(), sess.ID, "anchor")
	testutil.FailErr(t, "open checkpoint", err)
	writeProjectFile(t, root, "main.go", "before")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	operations := map[string]func() error{
		"open":         func() error { _, err := checkpoints.Open(canceled, sess.ID, "other"); return err },
		"read":         func() error { _, err := checkpoints.Load(canceled, sess.ID, "anchor"); return err },
		"capture":      func() error { return checkpoints.CapturePreImage(canceled, sess.ID, "anchor", "main.go") },
		"bind":         func() error { return checkpoints.CaptureBlueprintBinding(canceled, sess.ID, "anchor", "plan.md") },
		"truncate":     func() error { return checkpoints.MarkTruncated(canceled, sess.ID, "anchor") },
		"drop anchor":  func() error { return checkpoints.DropAnchor(canceled, sess.ID, "anchor") },
		"drop session": func() error { return checkpoints.DropSession(canceled, sess.ID) },
	}
	for name, operation := range operations {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s ignored cancellation: %v", name, err)
		}
	}
	manifest, err := checkpoints.Load(t.Context(), sess.ID, "anchor")
	testutil.FailErr(t, "read checkpoint after canceled mutations", err)
	if len(manifest.Paths) != 0 || manifest.Truncated || manifest.BlueprintPath != "" {
		t.Fatalf("canceled operation changed checkpoint: %+v", manifest)
	}
	if _, err := checkpoints.Load(t.Context(), sess.ID, "other"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatalf("canceled open published checkpoint: %v", err)
	}
}
