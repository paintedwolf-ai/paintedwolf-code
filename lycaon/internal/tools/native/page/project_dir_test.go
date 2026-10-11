package page

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestResolveCaptureProjectDirUsesWorkerWorkspaceView(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	testutil.FailErr(t, "write primary fixture", os.WriteFile(filepath.Join(primary, "index.html"), []byte("primary"), 0o600))
	testutil.FailErr(t, "write worker fixture", os.WriteFile(filepath.Join(branch, "index.html"), []byte("worker"), 0o600))

	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "project", Path: primary, IsPrimary: true}},
			ActiveRootID:     "project",
			WorkerBranchRoot: branch,
			BranchWorkspace:  testutil.CompleteBranchWorkspace{}},
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1"},
	}
	resolved, err := resolveCaptureProjectDir(t.Context(), tctx, ".")
	testutil.FailErr(t, "resolve worker capture root", err)
	if resolved != branch {
		t.Fatalf("capture root = %q want worker branch %q", resolved, branch)
	}
	raw, err := os.ReadFile(filepath.Join(resolved, "index.html"))
	testutil.FailErr(t, "read resolved capture entry", err)
	if string(raw) != "worker" {
		t.Fatalf("capture entry = %q want worker", raw)
	}
}

func TestResolveCaptureProjectDirUsesActiveAttachedRoot(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{
			{ID: "project", Label: "app", Path: primary, IsPrimary: true},
			{ID: "docs", Label: "docs", Path: secondary},
		},
			ActiveRootID: "project"},
	}
	resolved, err := resolveCaptureProjectDir(t.Context(), tctx, "@docs")
	testutil.FailErr(t, "resolve attached capture root", err)
	if resolved != secondary {
		t.Fatalf("capture root = %q want secondary %q", resolved, secondary)
	}
}

func TestCaptureHandlerReadsAndReturnsTheUnsavedWorkerSurface(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	primary := t.TempDir()
	branch := t.TempDir()
	primaryHTML := `<!doctype html><body style="margin:0;background:#d7263d;color:white">PRIMARY SURFACE</body>`
	workerHTML := `<!doctype html><body style="margin:0;background:#16a34a;color:white">WORKER DRAFT SURFACE</body>`
	testutil.FailErr(t, "write primary capture fixture", os.WriteFile(filepath.Join(primary, "index.html"), []byte(primaryHTML), 0o600))
	testutil.FailErr(t, "write worker capture fixture", os.WriteFile(filepath.Join(branch, "index.html"), []byte(workerHTML), 0o600))

	pool := browser.NewPool("")
	pool.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(pool.Close)
	out := &tools.ToolInvocationOut{}
	result, err := CaptureHandler(pool, nil, nil)(t.Context(), map[string]any{
		"project_dir": ".",
		"viewport":    map[string]any{"width": 320, "height": 180},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project",
			SessionID:       "worker",
			ParentSessionID: "root",
			WorkerJobID:     "job-1"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "project", Path: primary, IsPrimary: true}},
			ActiveRootID:     "project",
			WorkerBranchRoot: branch,
			BranchWorkspace:  testutil.CompleteBranchWorkspace{}},
		Effects: tools.InvocationEffects{Out: out},
	})
	testutil.FailErr(t, "capture worker draft surface", err)
	if !strings.Contains(result, "WORKER DRAFT SURFACE") || strings.Contains(result, "PRIMARY SURFACE") {
		t.Fatalf("capture result did not come from worker workspace: %s", result)
	}
	if out.Visual == nil || len(out.Visual.Bytes) == 0 || !out.Visual.Perceive || !out.Visual.Projected {
		t.Fatalf("worker capture visual = %+v", out.Visual)
	}
	img, err := png.Decode(bytes.NewReader(out.Visual.Bytes))
	testutil.FailErr(t, "decode worker capture", err)
	green, black := 0, 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if g > 0x7000 && r < 0x5000 && b < 0x6000 {
				green++
			}
			if r < 0x2500 && g < 0x2500 && b < 0x2500 {
				black++
			}
		}
	}
	if green < 40_000 || black > 2_000 {
		t.Fatalf("worker capture pixels green=%d black=%d; want the authored surface, not a black substitute", green, black)
	}
}
