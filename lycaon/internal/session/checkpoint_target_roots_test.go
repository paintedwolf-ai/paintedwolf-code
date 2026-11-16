package session_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

// The primary root is the one the project marks primary, wherever its id sorts.
func TestCheckpointTargetsNameThePrimaryRootByIdentity(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "a-docs", Label: "docs", Path: "/p/docs"},
		{ID: "z-app", Label: "app", Path: "/p/app", IsPrimary: true},
	}
	manifest := checkpoint.Manifest{Entries: []checkpoint.ManifestEntry{{Path: layeredfs.QualifiedPath{RootID: "a-docs", Rel: "x"}}}}
	targets, primary, err := session.TargetRootsForTaskCheckpoint(&api.WorkerTask{}, roots, manifest)
	if err != nil || primary != "z-app" || targets["z-app"] != "/p/app" || targets["a-docs"] != "/p/docs" || len(targets) != 2 {
		t.Fatalf("targets=%v primary=%q err=%v", targets, primary, err)
	}
	for _, alias := range []string{"primary", "root-0", ""} {
		manifest.Entries[0].Path.RootID = alias
		if _, _, err := session.TargetRootsForTaskCheckpoint(&api.WorkerTask{}, roots, manifest); !errors.Is(err, layeredfs.ErrInvalidRoot) {
			t.Errorf("alias %q resolved: %v", alias, err)
		}
	}
	if _, _, err := session.TargetRootsForTaskCheckpoint(&api.WorkerTask{}, nil, checkpoint.Manifest{}); !errors.Is(err, layeredfs.ErrInvalidRoot) {
		t.Errorf("a task with no roots and no root id resolved: %v", err)
	}
}
