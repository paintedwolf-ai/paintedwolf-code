package survey

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestListDirExplicitSkipDirTarget(t *testing.T) {
	tmpDir := t.TempDir()
	lycaonDir := filepath.Join(tmpDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.Mkdir(lycaonDir, 0o755))
	testutil.FailErr(t, "mkdir plans", os.Mkdir(filepath.Join(lycaonDir, "plans"), 0o755))
	testutil.FailErr(t, "write verify", os.WriteFile(filepath.Join(lycaonDir, "verify.yaml"), []byte("x"), 0o644))

	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": settingsoverlay.DirName()}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "list_dir overlay", err)
	resp := decodeListDirOut(t, out)
	if len(resp.Entries) == 0 {
		t.Fatalf("list_dir(.paintedwolf) returned no entries; out=%q", out)
	}
}
