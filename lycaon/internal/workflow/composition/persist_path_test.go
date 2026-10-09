package composition_test

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	"strings"
	"testing"
)

func TestResolvePersistFeatureDirUsesWorkflowID(t *testing.T) {
	got, err := workflowcomposition.ResolvePersistFeatureDir("hotfix-light")
	testutil.FailErr(t, "workflowcomposition.ResolvePersistFeatureDir failed", err)
	if got != "hotfix-light" {
		t.Fatalf("got %q", got)
	}
	if workflowcomposition.ProjectWorkflowOverlayPath(got) != settingsoverlay.DirName()+"/workflows/hotfix-light/workflow.yaml" {
		t.Fatalf("overlay path = %q", workflowcomposition.ProjectWorkflowOverlayPath(got))
	}
}

// TestResolvePersistFeatureDirRejectsTraversal: the id is the only input that
// reaches the filesystem, so the id pattern is the whole containment story.
func TestResolvePersistFeatureDirRejectsTraversal(t *testing.T) {
	for _, id := range []string{"../evil", "foo/../bar", "/abs", "nested/foo", ".", ".."} {
		if _, err := workflowcomposition.ResolvePersistFeatureDir(id); err == nil {
			t.Fatalf("expected error for %q", id)
		}
	}
}

func TestResolvePersistFeatureDirRejectsInvalidID(t *testing.T) {
	if _, err := workflowcomposition.ResolvePersistFeatureDir("Bad-ID"); err == nil {
		t.Fatal("expected invalid id error")
	}
	if !strings.Contains(errInvalidID(t, "Bad-ID"), "must match") {
		t.Fatal("expected pattern message")
	}
}

func errInvalidID(t *testing.T, id string) string {
	t.Helper()
	_, err := workflowcomposition.ResolvePersistFeatureDir(id)
	if err == nil {
		t.Fatal("expected error")
	}
	return err.Error()
}
