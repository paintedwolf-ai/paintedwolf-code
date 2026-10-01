package store_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestChildSessionPostureImplementerUsesBuild(t *testing.T) {
	got := store.ChildSessionPosture(api.SessionPostureSpec, orchestration.ProfileImplementer)
	if got != api.SessionPostureBuild {
		t.Fatalf("posture = %q want build", got)
	}
}

func TestChildSessionPostureExplorerInheritsSpec(t *testing.T) {
	got := store.ChildSessionPosture(api.SessionPostureSpec, orchestration.ProfilePathExplorer)
	if got != api.SessionPostureSpec {
		t.Fatalf("posture = %q want spec", got)
	}
}
