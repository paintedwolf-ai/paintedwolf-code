package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rod/rod"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "test", "fixtures", "capture-page", name))
	testutil.FailErr(t, "fixture abs path", err)
	if _, err := os.Stat(root); err != nil {
		testutil.FailErr(t, "stat capture fixture "+name, err)
	}
	return root
}

func newCaptureTestPool(cacheDir string) *Pool {
	pool := NewPool(cacheDir)
	pool.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	return pool
}

func TestUnavailableTextGeometryDoesNotInvalidateTheProtectedCapture(t *testing.T) {
	regions := availablePageRegions(nil)
	if regions.Complete || len(regions.Regions) != 0 {
		t.Fatalf("unavailable geometry = %+v", regions)
	}
}

func TestAvailablePageRegionsRetriesTransientCollectionErrors(t *testing.T) {
	attempts := 0
	want := PageRegions{Complete: true, Width: 400, Height: 300}
	got := availablePageRegionsWith(nil, func(*rod.Page) (PageRegions, error) {
		attempts++
		if attempts < pageRegionCollectionAttempts {
			return PageRegions{}, errors.New("transient CDP read")
		}
		return want, nil
	})
	if got.Complete != want.Complete || got.Width != want.Width ||
		got.Height != want.Height || len(got.Regions) != 0 {
		t.Fatalf("available regions = %+v, want %+v", got, want)
	}
	if attempts != pageRegionCollectionAttempts {
		t.Fatalf("collection attempts = %d, want %d", attempts, pageRegionCollectionAttempts)
	}
}

func TestCaptureProjectDirClean(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "clean"),
		Width:      400,
		Height:     300,
	})
	testutil.FailErr(t, "capture clean project", err)
	if len(out.Bytes) == 0 {
		t.Fatal("expected png bytes")
	}
	if out.Mime != "image/png" {
		t.Fatalf("mime %q", out.Mime)
	}
	var state map[string]any
	err = json.Unmarshal(out.State, &state)
	testutil.FailErr(t, "unmarshal capture state", err)
	if title, _ := state["title"].(string); title != "Clean" && state["url"] == nil {
		t.Fatalf("state missing title/url: %#v", state)
	}
}

func TestCaptureConsoleErrorEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "console-error"),
		Width:      400,
		Height:     300,
	})
	testutil.FailErr(t, "capture console error evidence", err)
	joined := strings.Join(out.Log, "\n")
	if !strings.Contains(joined, "fixture boom") {
		t.Fatalf("expected the fixture's console error in log, got %q", joined)
	}
	if len(out.Errors) == 0 || !strings.Contains(out.Errors[0].Message, "runtime fixture error") || out.Errors[0].Source == "" {
		t.Fatalf("expected the uncaught error with its source, got %+v", out.Errors)
	}
}

func TestCaptureProjectsSecretFromPixelsSemanticEvidenceLogsAndCaption(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	const secret = "capture-secret-value"
	out, regions := captureSecretCanvasFixture(t, secret)
	assertExactTextGeometry(t, regions, "DOM LEFT "+secret+" DOM RIGHT", secret)
	assertExactTextGeometryContaining(t, regions, "INPUT LEFT "+secret, secret)
	canvasText := "CANVAS LEFT " + secret + " CANVAS RIGHT"
	canvasRegion := exactTextRegion(t, regions, canvasText, secret)
	for _, region := range regions.Regions {
		if strings.Contains(region.Text, "FORGED") || strings.Contains(region.Text, "STALE") {
			t.Fatalf("capture used page-owned or cleared canvas bookkeeping: %+v", region)
		}
	}
	evidence, err := json.Marshal(out.PageEvidence)
	testutil.FailErr(t, "encode page evidence", err)
	visible := string(out.State) + string(out.Snapshot) + string(evidence) + out.Caption
	if strings.Contains(visible, secret) {
		t.Fatalf("capture projection retained secret: %s", visible)
	}
	if !strings.Contains(visible, "[REDACTED]") {
		t.Fatalf("capture projection omitted redaction marker: %s", visible)
	}
	if len(out.Bytes) == 0 {
		t.Fatal("screened raster is empty")
	}
	if out.Coverage == nil || !out.Coverage.Structured {
		t.Fatalf("canvas coverage = %+v", out.Coverage)
	}
	assertCanvasPixelsPreservedAndOnlySecretTextScreened(t, out, canvasRegion, secret)
}

func TestCapturePreservesCanvasAfterTextGeometryIsInvalidated(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	const secret = "capture-secret-value"
	dir := t.TempDir()
	html := `<!doctype html><body style="margin:0">
		<canvas id="surface" width="400" height="140"></canvas>
		<script>
		  const canvas = document.querySelector('#surface');
		  const ctx = canvas.getContext('2d');
		  ctx.font = '20px monospace';
		  ctx.fillText('` + secret + `', 8, 40);
		  ctx.fillStyle = '#2563eb';
		  ctx.fillRect(0, 0, 400, 140);
		</script></body>`
	testutil.FailErr(t, "write invalidated canvas fixture", os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o600))
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret: secret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		}}
	})
	pool := NewPool("")
	pool.SetCaptureProjector(captureprojection.New(m, nil))
	defer pool.Close()
	var (
		regions   PageRegions
		regionErr error
	)
	out, err := (&CapturePool{Pool: pool}).Capture(t.Context(), CaptureRequest{
		ProjectDir: dir, Width: 400, Height: 200,
		Preview: &CapturePreview{Attach: func(held *HeldPage) {
			regions, regionErr = CollectPageRegions(held.Page)
		}},
	})
	testutil.FailErr(t, "capture invalidated canvas fixture", err)
	testutil.FailErr(t, "collect invalidated canvas regions", regionErr)
	if regions.Complete || out.Coverage == nil || out.Coverage.Structured {
		t.Fatalf("invalidated canvas coverage regions=%+v capture=%+v", regions, out.Coverage)
	}
	for _, region := range regions.Regions {
		if strings.Contains(region.Text, secret) {
			t.Fatalf("invalidated canvas text remained maskable: %+v", region)
		}
	}
	img, err := png.Decode(bytes.NewReader(out.Bytes))
	testutil.FailErr(t, "decode invalidated canvas capture", err)
	blue, black := 0, 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 0x4000 && g > 0x4000 && g < 0x9000 && b > 0xc000 {
				blue++
			}
			if r < 0x1800 && g < 0x1800 && b < 0x1800 {
				black++
			}
		}
	}
	if blue < 40_000 || black > 100 {
		t.Fatalf("invalidated canvas pixels blue=%d black=%d", blue, black)
	}
}

func captureSecretCanvasFixture(t *testing.T, secret string) (CaptureResult, PageRegions) {
	t.Helper()
	dir := t.TempDir()
	html := `<!doctype html><html><head><title>` + secret + `</title></head><body style="margin:0">
		<p style="font:20px sans-serif;margin:4px">DOM LEFT ` + secret + ` DOM RIGHT</p>
		<input id="token" style="font:20px sans-serif;width:390px" value="INPUT LEFT ` + secret + ` INPUT RIGHT">
		<canvas id="game" width="400" height="140" style="position:absolute;left:0;top:120px"></canvas>
		<script>
		  const canvas = document.querySelector('#game');
		  canvas.__pwCanvasTextRuns = [{text: 'FORGED ` + secret + `', spans: [{start: 0, end: 1, rects: [{x: 0, y: 0, width: 400, height: 140}]}]}];
		  const ctx = canvas.getContext('2d');
		  ctx.fillText('STALE ` + secret + `', 8, 20); ctx.clearRect(0, 0, 400, 140);
		  ctx.fillStyle = '#d7263d'; ctx.fillRect(0, 0, 400, 140);
		  ctx.fillStyle = '#fff'; ctx.font = '18px monospace'; ctx.fillText('CANVAS LEFT ` + secret + ` CANVAS RIGHT', 8, 70);
		  console.log("` + secret + `");
		</script></body></html>`
	testutil.FailErr(t, "write capture fixture", os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o600))
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret: secret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		}}
	})
	pool := NewPool("")
	pool.SetCaptureProjector(captureprojection.New(m, nil))
	defer pool.Close()
	var (
		regions   PageRegions
		regionErr error
	)
	out, err := (&CapturePool{Pool: pool}).Capture(context.Background(), CaptureRequest{
		ProjectDir: dir, Caption: secret, Width: 400, Height: 300,
		CaptureScope: captureprojection.Scope{ProjectID: "project", RootSessionID: "root", SessionID: "session"},
		Preview: &CapturePreview{Attach: func(held *HeldPage) {
			regions, regionErr = CollectPageRegions(held.Page)
		}},
	})
	testutil.FailErr(t, "capture secret canvas fixture", err)
	testutil.FailErr(t, "collect canvas text regions", regionErr)
	return out, regions
}

func exactTextRegion(t *testing.T, regions PageRegions, text, secret string) captureprojection.Region {
	t.Helper()
	for _, region := range regions.Regions {
		if region.Text != text {
			continue
		}
		secretByteStart := strings.Index(text, secret)
		if secretByteStart < 0 {
			t.Fatalf("secret is absent from %q", text)
		}
		secretStart := len([]rune(text[:secretByteStart]))
		secretEnd := secretStart + len([]rune(secret))
		seen := make(map[int]bool, len(region.Spans))
		for _, span := range region.Spans {
			if span.Start < 0 || span.End != span.Start+1 || len(span.Rects) == 0 {
				t.Fatalf("invalid span = %+v for %q", span, text)
			}
			seen[span.Start] = true
		}
		if secretStart == 0 || secretEnd >= len([]rune(text)) {
			t.Fatalf("fixture does not surround secret in %q", text)
		}
		for i := secretStart; i < secretEnd; i++ {
			if !seen[i] {
				t.Fatalf("secret rune %d has no exact geometry in %q", i, text)
			}
		}
		safeBefore, safeAfter := false, false
		for offset := range seen {
			safeBefore = safeBefore || offset < secretStart
			safeAfter = safeAfter || offset >= secretEnd
		}
		if !safeBefore || !safeAfter {
			t.Fatalf("fixture has no safe geometry around secret in %q", text)
		}
		return region
	}
	t.Fatalf("exact text geometry was not inventoried for %q: %+v", text, regions)
	return captureprojection.Region{}
}

func assertExactTextGeometry(t *testing.T, regions PageRegions, text, secret string) {
	t.Helper()
	exactTextRegion(t, regions, text, secret)
}

func assertExactTextGeometryContaining(t *testing.T, regions PageRegions, text, secret string) {
	t.Helper()
	for _, region := range regions.Regions {
		if strings.Contains(region.Text, text) {
			exactTextRegion(t, regions, region.Text, secret)
			return
		}
	}
	t.Fatalf("text geometry containing %q was not inventoried: %+v", text, regions)
}

func assertCanvasPixelsPreservedAndOnlySecretTextScreened(
	t *testing.T, out CaptureResult, region captureprojection.Region, secret string,
) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(out.Bytes))
	testutil.FailErr(t, "decode screened raster", err)
	canvasRight := 400 * img.Bounds().Dx() / out.Width
	canvasTop := 120 * img.Bounds().Dy() / out.Height
	canvasBottom := 260 * img.Bounds().Dy() / out.Height
	redPixels := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 0xb000 && g < 0x5000 && b < 0x6000 {
				redPixels++
			}
		}
	}
	if redPixels < 25_000 {
		t.Fatalf("canvas pixels were erased: only %d red pixels survived", redPixels)
	}
	secretStart := strings.Index(region.Text, secret)
	secretEnd := secretStart + len(secret)
	for _, span := range region.Spans {
		for _, rect := range span.Rects {
			centerX := int((rect.X + rect.Width/2) * float64(img.Bounds().Dx()) / float64(out.Width))
			centerY := int((rect.Y + rect.Height/2) * float64(img.Bounds().Dy()) / float64(out.Height))
			if centerX < 0 || centerX >= canvasRight || centerY < canvasTop || centerY >= canvasBottom {
				continue
			}
			r, g, b, _ := img.At(centerX, centerY).RGBA()
			masked := r < 0x3000 && g < 0x3000 && b < 0x4000
			if span.Start >= secretStart && span.Start < secretEnd && !masked {
				t.Fatalf("secret canvas rune %d at (%d,%d) was not screened", span.Start, centerX, centerY)
			}
			if (span.Start < secretStart || span.Start >= secretEnd) && masked {
				t.Fatalf("safe canvas rune %d at (%d,%d) was over-masked", span.Start, centerX, centerY)
			}
		}
	}
}

func TestCaptureIdleWaitsForAsyncPaint(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "async-paint"),
		Wait:       "idle",
		Width:      400,
		Height:     300,
	})
	testutil.FailErr(t, "capture async paint", err)
	var snap map[string]any
	err = json.Unmarshal(out.Snapshot, &snap)
	testutil.FailErr(t, "unmarshal capture snapshot", err)
	raw, err := json.Marshal(snap)
	testutil.FailErr(t, "marshal async paint snapshot", err)
	if !strings.Contains(string(raw), "fully painted") {
		t.Fatalf("snapshot missing fully painted text: %s", raw)
	}
}

func TestCaptureReactiveFillThenClick(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "reactive-form"),
		Actions: []CaptureAction{
			{Type: "fill", ActionLocator: ActionLocator{Selector: "#name"}, Value: "Ada"},
			{Type: "click", ActionLocator: ActionLocator{Selector: "#submit"}},
			{Type: "wait_for", ActionLocator: ActionLocator{Text: "submitted:Ada"}, TimeoutMS: 5000},
		},
		Width:  400,
		Height: 300,
	})
	testutil.FailErr(t, "capture reactive form", err)
	var snap map[string]any
	testutil.FailErr(t, "unmarshal reactive snapshot", json.Unmarshal(out.Snapshot, &snap))
	raw, err := json.Marshal(snap)
	testutil.FailErr(t, "marshal reactive snapshot", err)
	if !strings.Contains(string(raw), "submitted:Ada") {
		t.Fatalf("expected submitted state in snapshot: %s", raw)
	}
}

func TestCaptureAbsoluteAssetPathFulfill(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}

	home, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "absolute-assets"),
		Width:      400,
		Height:     300,
	})
	testutil.FailErr(t, "capture absolute asset home", err)
	assertNoFailedRequests(t, "home", home.Network)

	talks, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "absolute-assets"),
		Path:       "/talks/",
		Width:      400,
		Height:     300,
	})
	testutil.FailErr(t, "capture absolute asset route", err)
	if !strings.Contains(talks.FinalURL, "/talks") {
		t.Fatalf("final_url=%q", talks.FinalURL)
	}
	assertNoFailedRequests(t, "talks", talks.Network)
	var state map[string]any
	err = json.Unmarshal(talks.State, &state)
	testutil.FailErr(t, "unmarshal talks state", err)
	if title, _ := state["title"].(string); title != "Absolute talks" {
		t.Fatalf("title=%q state=%#v", title, state)
	}
}

func TestCaptureRejectsFileURL(t *testing.T) {
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	_, err := cap.Capture(context.Background(), CaptureRequest{
		URL: "file:///etc/passwd",
	})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_NAVIGATION_DENIED" {
		t.Fatalf("got %#v want CAPTURE_NAVIGATION_DENIED", err)
	}
}

func TestCaptureRejectsMissingTarget(t *testing.T) {
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	_, err := cap.Capture(context.Background(), CaptureRequest{})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_TARGET_INVALID" {
		t.Fatalf("got %#v want CAPTURE_TARGET_INVALID", err)
	}
}

func TestCaptureRoleLabelClicksBareButtonAndServesStaticJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "role-label-button"),
		Actions: []CaptureAction{
			{Type: "click", ActionLocator: ActionLocator{Role: "button", Label: "Check configuration"}, Wait: "idle"},
			{Type: "wait_for", ActionLocator: ActionLocator{Text: "ok:static"}, TimeoutMS: 5000},
		},
		Width:  400,
		Height: 300,
	})
	testutil.FailErr(t, "capture role-label action", err)
	var snap map[string]any
	err = json.Unmarshal(out.Snapshot, &snap)
	testutil.FailErr(t, "unmarshal capture snapshot", err)
	raw, err := json.Marshal(snap)
	testutil.FailErr(t, "marshal role-label snapshot", err)
	if !strings.Contains(string(raw), "ok:static") {
		t.Fatalf("snapshot missing static fetch result: %s", raw)
	}
}

func TestCaptureActionFailedIncludesLocatorEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	_, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "role-label-button"),
		Actions: []CaptureAction{
			{Type: "click", ActionLocator: ActionLocator{Role: "button", Label: "Missing control"}},
		},
		Width:  400,
		Height: 300,
	})
	if err == nil {
		t.Fatal("expected CAPTURE_ACTION_FAILED")
	}
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FAILED" {
		t.Fatalf("got %#v want CAPTURE_ACTION_FAILED", err)
	}
	if rej.Data["error"] != "target not found" {
		t.Fatalf("error = %#v", rej.Data["error"])
	}
	locs, _ := rej.Data["locators"].(string)
	if !strings.Contains(locs, "role=button") || !strings.Contains(locs, "Missing control") {
		t.Fatalf("locators = %q", locs)
	}
	interactive, _ := rej.Data["interactive"].(string)
	if !strings.Contains(interactive, "Check configuration") {
		t.Fatalf("interactive = %q", interactive)
	}
}

func TestCaptureURLReachesHostLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	confine.TestingSetAutoConfine(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Loopback Capture</title></head><body><h1>ok</h1></body></html>`))
	}))
	t.Cleanup(srv.Close)

	pool := newCaptureTestPool("")
	defer pool.Close()
	out, err := (&CapturePool{Pool: pool}).Capture(context.Background(), CaptureRequest{
		URL:    srv.URL,
		Width:  400,
		Height: 300,
	})
	testutil.FailErr(t, "capture host loopback", err)
	if len(out.Bytes) == 0 {
		t.Fatal("expected png bytes")
	}
	var state map[string]any
	err = json.Unmarshal(out.State, &state)
	testutil.FailErr(t, "unmarshal capture state", err)
	if title, _ := state["title"].(string); title != "Loopback Capture" {
		t.Fatalf("state title = %#v", state["title"])
	}
}

func TestSameOrigin(t *testing.T) {
	if !SameOrigin("http://127.0.0.1:1234/", "http://127.0.0.1:1234/foo") {
		t.Fatal("same origin expected")
	}
	if SameOrigin("http://127.0.0.1:1234/", "http://evil.test/") {
		t.Fatal("cross origin should fail")
	}
}

func TestConsoleLineIsScreenedBeforeTheLineCapClipsIt(t *testing.T) {
	const secret = "capture-secret-value"
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret: secret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		}}
	})
	projector := captureprojection.New(m, nil)
	ev := &pageEvidence{screen: func(line string) (string, error) {
		projected, err := projector.Text(
			context.Background(), captureprojection.Scope{}, "capture.browser.log", line,
		)
		return projected.Value, err
	}}
	// Screen before clipping across the value boundary.
	ev.appendConsole(strings.Repeat("x", MaxConsoleLogLineBytes-10) + secret + "tail")

	lines := ev.since(evidenceMark{}).Log
	if len(lines) != 1 {
		t.Fatalf("retained lines = %d, want 1", len(lines))
	}
	if strings.Contains(lines[0], secret[:10]) {
		t.Fatalf("clipped console line kept the head of a protected value: %q", lines[0][len(lines[0])-40:])
	}
	if !strings.Contains(lines[0], "[REDACTED]") {
		t.Fatalf("console line was not screened: %q", lines[0][len(lines[0])-40:])
	}
}

func TestConsoleLineWithoutScreeningIsWithheld(t *testing.T) {
	ev := &pageEvidence{}
	ev.appendConsole("console.log: capture-secret-value")
	lines := ev.since(evidenceMark{}).Log
	if len(lines) != 1 || lines[0] != captureUnavailableLine {
		t.Fatalf("unscreened console lines = %q", lines)
	}
}

// assertNoFailedRequests requires every request a capture made to have been answered.
func assertNoFailedRequests(t *testing.T, label string, network []NetworkRecord) {
	t.Helper()
	for _, r := range network {
		if r.failed() {
			t.Fatalf("%s: request %s %s failed (status %d, failure %q); all=%+v", label, r.Method, r.URL, r.Status, r.Failure, network)
		}
	}
}
