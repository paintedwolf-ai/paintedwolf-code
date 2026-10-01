package browser

import (
	"context"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureHermeticGoogleFonts(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)

	root := t.TempDir()
	html := `<!doctype html><html><head>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;700&family=JetBrains+Mono:wght@400&display=swap" rel="stylesheet">
<style>body{font-family:Inter,sans-serif} code{font-family:"JetBrains Mono",monospace}</style>
</head><body><p id="p">Hello</p><code id="c">mono</code></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(html), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	pool := newCaptureTestPool(filepath.Join(t.TempDir(), "browser-cache"))
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: root,
		Width:      400,
		Height:     300,
		Caption:    "fonts",
	})
	testutil.FailErr(t, "cap.Capture failed", err)
	fontRequests := 0
	for _, r := range out.Network {
		if !strings.Contains(r.URL, "fonts.googleapis.com") && !strings.Contains(r.URL, "fonts.gstatic.com") && !strings.Contains(r.URL, "/fonts/") {
			continue
		}
		fontRequests++
		if r.failed() || r.ServedBy != networkServedByFont {
			t.Fatalf("font request %s was not answered by the bundled substitute: %+v", r.URL, r)
		}
	}
	if fontRequests == 0 {
		t.Fatalf("capture made no font requests: %+v", out.Network)
	}

	page, err := pool.NewPage(context.Background(), 400, 300)
	testutil.FailErr(t, "pool.NewPage failed", err)
	defer func() { _ = page.Close() }()
	mount, err := AttachCaptureFetch(t.Context(), page, root, newRouteTable(nil), nil)
	testutil.FailErr(t, "AttachCaptureFetch failed", err)
	defer func() { _ = mount.Close(t.Context()) }()
	if err := page.Navigate(StaticOrigin); err != nil {
		testutil.FailErr(t, "page.Navigate failed", err)
	}
	_ = page.WaitLoad()
	res, err := page.Eval(`async () => {
	  await document.fonts.ready;
	  const inter = document.getElementById('p');
	  const mono = document.getElementById('c');
	  return {
	    inter: getComputedStyle(inter).fontFamily,
	    mono: getComputedStyle(mono).fontFamily,
	    interLoaded: document.fonts.check('16px Inter'),
	    monoLoaded: document.fonts.check('16px "JetBrains Mono"'),
	    sheets: [...document.styleSheets].map(s => s.href),
	  };
	}`)
	testutil.FailErr(t, "page.Eval failed", err)
	val := res.Value.Map()
	inter := val["inter"].Str()
	mono := val["mono"].Str()
	if !strings.Contains(inter, "Inter") {
		t.Fatalf("inter family=%q val=%v", inter, res.Value)
	}
	if !strings.Contains(mono, "JetBrains Mono") {
		t.Fatalf("mono family=%q val=%v", mono, res.Value)
	}
	if !val["interLoaded"].Bool() || !val["monoLoaded"].Bool() {
		t.Fatalf("fonts not loaded: %v", res.Value)
	}
}
