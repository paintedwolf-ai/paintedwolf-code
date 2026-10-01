package browser

import (
	"bytes"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestPNG(t *testing.T, path string) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	testutil.FailErr(t, "encode png", png.Encode(&buf, img))
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write png", os.WriteFile(path, buf.Bytes(), 0o644))
	return buf.Bytes()
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(path, []byte(content), 0o644))
}

func newTestAssetMount(t *testing.T, root string, budgets RenderBudgets) *AssetMount {
	t.Helper()
	m, err := NewAssetMount(root, budgets)
	testutil.FailErr(t, "NewAssetMount", err)
	return m
}

// testRenderBudgets loads the shipped caps from the embedded config.
func testRenderBudgets(t *testing.T) RenderBudgets {
	t.Helper()
	budgets, err := LoadRenderBudgets()
	testutil.FailErr(t, "LoadRenderBudgets", err)
	return budgets
}

func wantAssetReject(t *testing.T, rej *browserengine.RejectError, code string) {
	t.Helper()
	if rej == nil {
		t.Fatalf("expected %s, got success", code)
	}
	if rej.Code != code {
		t.Fatalf("code: got %q (%v) want %q", rej.Code, rej.Data, code)
	}
}

func TestAssetServePNG(t *testing.T) {
	root := t.TempDir()
	raw := writeTestPNG(t, filepath.Join(root, "assets", "logo.png"))
	m := newTestAssetMount(t, root, testRenderBudgets(t))
	body, ctype, rej := m.serveAsset("/assets/logo.png")
	if rej != nil {
		t.Fatalf("serve: %v", rej)
	}
	if !bytes.Equal(body, raw) {
		t.Fatal("body mismatch")
	}
	if !strings.HasPrefix(ctype, "image/png") {
		t.Fatalf("ctype: %q", ctype)
	}
	sum := m.Summary()
	if sum == nil || sum.Count != 1 || sum.SamplePaths[0] != "assets/logo.png" {
		t.Fatalf("summary: %+v", sum)
	}
}

func TestAssetMissingNotFound(t *testing.T) {
	m := newTestAssetMount(t, t.TempDir(), testRenderBudgets(t))
	_, _, rej := m.serveAsset("/assets/nope.png")
	wantAssetReject(t, rej, "RENDER_ASSET_NOT_FOUND")
}

func TestAssetNoProjectRoot(t *testing.T) {
	m := newTestAssetMount(t, "", testRenderBudgets(t))
	_, _, rej := m.serveAsset("/assets/logo.png")
	wantAssetReject(t, rej, "RENDER_ASSET_NOT_FOUND")
}

func TestAssetGitPathDenied(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main")
	m := newTestAssetMount(t, root, testRenderBudgets(t))
	_, _, rej := m.serveAsset("/.git/HEAD")
	wantAssetReject(t, rej, "RENDER_ASSET_DENIED")
}

func TestAssetSymlinkEscapeDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestPNG(t, filepath.Join(outside, "secret.png"))
	testutil.FailErr(t, "symlink", os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(root, "leak.png")))
	m := newTestAssetMount(t, root, testRenderBudgets(t))
	_, _, rej := m.serveAsset("/leak.png")
	wantAssetReject(t, rej, "RENDER_ASSET_DENIED")
}

func TestAssetPerFileCap(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "big.png"), strings.Repeat("x", 64))
	budgets := testRenderBudgets(t)
	budgets.MaxAssetBytes = 16
	m := newTestAssetMount(t, root, budgets)
	_, _, rej := m.serveAsset("/big.png")
	wantAssetReject(t, rej, "RENDER_ASSET_TOO_LARGE")
}

func TestAssetRenderTotalCap(t *testing.T) {
	root := t.TempDir()
	a := writeTestPNG(t, filepath.Join(root, "a.png"))
	writeTestPNG(t, filepath.Join(root, "b.png"))
	budgets := testRenderBudgets(t)
	budgets.MaxAssetBytes = len(a)
	budgets.MaxRenderBytes = len(a) + 1
	m := newTestAssetMount(t, root, budgets)
	if _, _, rej := m.serveAsset("/a.png"); rej != nil {
		t.Fatalf("first serve: %v", rej)
	}
	_, _, rej := m.serveAsset("/b.png")
	wantAssetReject(t, rej, "RENDER_ASSET_TOO_LARGE")
}

func TestAssetHTMLTypeRejected(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "page.html"), "<!DOCTYPE html><html><body>hi</body></html>")
	m := newTestAssetMount(t, root, testRenderBudgets(t))
	_, _, rej := m.serveAsset("/page.html")
	wantAssetReject(t, rej, "RENDER_ASSET_TYPE")
}

func TestAssetSVGServedAndSanitized(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "ok.svg"), `<svg xmlns="http://www.w3.org/2000/svg"><rect width="4" height="4"/></svg>`)
	writeTestFile(t, filepath.Join(root, "evil.svg"), `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	writeTestFile(t, filepath.Join(root, "foreign.svg"), `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div>x</div></foreignObject></svg>`)
	m := newTestAssetMount(t, root, testRenderBudgets(t))
	_, ctype, rej := m.serveAsset("/ok.svg")
	if rej != nil || ctype != "image/svg+xml" {
		t.Fatalf("ok.svg: ctype=%q rej=%v", ctype, rej)
	}
	_, _, rej = m.serveAsset("/evil.svg")
	wantAssetReject(t, rej, "RENDER_ASSET_DENIED")
	_, _, rej = m.serveAsset("/foreign.svg")
	wantAssetReject(t, rej, "RENDER_ASSET_DENIED")
}

func TestAssetCSSServedAndSanitized(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "styles", "app.css"),
		".hero{background:url(../assets/bg.png);color:#123}\n@import url(http://lycaon.asset/styles/base.css);")
	writeTestFile(t, filepath.Join(root, "styles", "cdn.css"), `.x{background:url(https://cdn.example/bg.png)}`)
	writeTestFile(t, filepath.Join(root, "styles", "imp.css"), `@import url(https://cdn.example/font.css);`)
	m := newTestAssetMount(t, root, testRenderBudgets(t))
	_, ctype, rej := m.serveAsset("/styles/app.css")
	if rej != nil || !strings.HasPrefix(ctype, "text/css") {
		t.Fatalf("app.css: ctype=%q rej=%v", ctype, rej)
	}
	_, _, rej = m.serveAsset("/styles/cdn.css")
	wantAssetReject(t, rej, "RENDER_ASSET_DENIED")
	_, _, rej = m.serveAsset("/styles/imp.css")
	wantAssetReject(t, rej, "RENDER_ASSET_DENIED")
}

func TestAssetFirstErrorRecordedOnce(t *testing.T) {
	m := newTestAssetMount(t, t.TempDir(), testRenderBudgets(t))
	m.recordError(&browserengine.RejectError{Code: "RENDER_ASSET_NOT_FOUND", Data: map[string]any{"path": "a.png"}})
	m.recordError(&browserengine.RejectError{Code: "RENDER_ASSET_DENIED", Data: map[string]any{"path": "b.png"}})
	if got := m.FirstError(); got == nil || got.Code != "RENDER_ASSET_NOT_FOUND" {
		t.Fatalf("first error: %+v", got)
	}
}

func TestAssetSummarySampleCap(t *testing.T) {
	root := t.TempDir()
	budgets := testRenderBudgets(t)
	budgets.MaxCatalogSamples = 2
	m := newTestAssetMount(t, root, budgets)
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		writeTestPNG(t, filepath.Join(root, name))
		if _, _, rej := m.serveAsset("/" + name); rej != nil {
			t.Fatalf("serve %s: %v", name, rej)
		}
	}
	sum := m.Summary()
	if sum == nil || sum.Count != 3 || len(sum.SamplePaths) != 2 {
		t.Fatalf("summary: %+v", sum)
	}
}

func TestAssetReadRemainsBoundedBeyondStat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset.bin")
	writeTestFile(t, path, strings.Repeat("x", 4096))
	body, err := readRenderAsset(path, 128)
	testutil.FailErr(t, "read bounded asset", err)
	if len(body) != 129 {
		t.Fatalf("read %d bytes, want cap plus one sentinel", len(body))
	}
}

func TestRenderLoadsOnlyProjectAssets(t *testing.T) {
	for ref, want := range map[string]bool{
		AssetOrigin + "img/logo.png":   true,
		"//lycaon.asset/img/logo.png":  true,
		"https://cdn.example/logo.png": false,
		"logo.png":                     false,
		"/fonts/inter.woff2":           false,
		"file:///etc/hosts":            false,
	} {
		if got := RenderLoadsReference(ref); got != want {
			t.Errorf("RenderLoadsReference(%q) = %v, want %v", ref, got, want)
		}
	}
}
