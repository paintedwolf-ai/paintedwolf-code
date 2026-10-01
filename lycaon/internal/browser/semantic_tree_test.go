package browser

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSnapshotTreeOmitsNonRenderingText(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()

	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "script-in-body"),
		Width:      400, Height: 300,
	})
	testutil.FailErr(t, "capture script fixture", err)
	tree := string(out.Snapshot)

	for _, leak := range []string{
		"SCRIPT_SOURCE_MUST_NOT_APPEAR_IN_TREE",
		"document.getElementById",
		"createElement",
		"color: #333",
	} {
		if strings.Contains(tree, leak) {
			t.Fatalf("snapshot tree leaked non-rendering text %q:\n%s", leak, tree)
		}
	}
	// Filtering preserves content produced by scripts.
	if !strings.Contains(tree, "rendered row") {
		t.Fatalf("snapshot tree dropped rendered content:\n%s", tree)
	}
}
