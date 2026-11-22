package native

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/page"
)

// render_view writes its dest as a session-folder path: @label writes into any
// attached root, and an unknown label is refused without writing.
func TestRenderViewDestResolvesAcrossRoots(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "lib")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
	}
	tctx := tools.ToolContext{Source: tools.InvocationSource{
		Roots: []projectroot.RootRef{
			{ID: "p", Label: "app", Path: primary, IsPrimary: true},
			{ID: "s", Label: "lib", Path: secondary},
		},
		ActiveRootID: "p",
	}}
	write := scopedWriter(nil, page.RenderViewToolName)

	testutil.FailErr(t, "write labelled dest", write(t.Context(), tctx, "@lib/view.png", []byte("png-bytes")))
	got, err := os.ReadFile(filepath.Join(secondary, "view.png"))
	testutil.FailErr(t, "read written dest", err)
	if string(got) != "png-bytes" {
		t.Fatalf("dest bytes = %q", got)
	}

	err = write(t.Context(), tctx, "@missing/view.png", []byte("png-bytes"))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "UNKNOWN_ROOT_LABEL" {
		t.Fatalf("dest with an unknown label = %v, want UNKNOWN_ROOT_LABEL", err)
	}
	for _, dir := range []string{primary, filepath.Join(primary, "@missing")} {
		if _, err := os.Stat(filepath.Join(dir, "view.png")); !os.IsNotExist(err) {
			t.Fatalf("refused dest wrote %s: %v", filepath.Join(dir, "view.png"), err)
		}
	}
}
