package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestActionsSurviveDocumentNavigation(t *testing.T) {
	if testing.Short() {
		t.Skip("browser navigation")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	t.Cleanup(pool.Close)
	for _, mode := range []string{"held", CaptureModeScreenshot, CaptureModeFilmstrip} {
		t.Run(mode, func(t *testing.T) {
			var visits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.URL.Path == "/next" {
					visits.Add(1)
					_, _ = fmt.Fprint(w, `<title>Next document</title><label>Name<input id="name"></label><button id="save" onclick="document.querySelector('output').textContent=document.querySelector('input').value">Save</button><output></output>`)
					return
				}
				_, _ = fmt.Fprint(w, `<title>First document</title><a href="/next">Next page</a>`)
			}))
			t.Cleanup(server.Close)
			actions := []CaptureAction{
				{Type: "click", ActionLocator: ActionLocator{Role: "link", Text: "Next page"}, Wait: "idle"},
				{Type: "fill", ActionLocator: ActionLocator{Selector: "#name"}, Value: "navigation retained café"},
				{Type: "click", ActionLocator: ActionLocator{Selector: "#save"}, Wait: "idle"},
			}
			var out CaptureResult
			if mode == "held" {
				held, err := OpenHeld(t.Context(), pool, OpenOpts{URL: server.URL, Width: 400, Height: 300})
				testutil.FailErr(t, "open held page", err)
				t.Cleanup(func() { _ = held.Close(t.Context()) })
				_, err = held.Snapshot(t.Context(), SnapshotOpts{})
				testutil.FailErr(t, "snapshot before navigation", err)
				_, err = held.Act(t.Context(), actions[:1], nil)
				testutil.FailErr(t, "navigate held page", err)
				_, err = held.Act(t.Context(), actions[1:], nil)
				testutil.FailErr(t, "act on new document", err)
				out, err = held.Snapshot(t.Context(), SnapshotOpts{})
				testutil.FailErr(t, "snapshot after navigation", err)
			} else {
				var err error
				out, err = (&CapturePool{Pool: pool}).Capture(t.Context(), CaptureRequest{
					URL: server.URL, Width: 400, Height: 300, Mode: mode, Actions: actions,
				})
				testutil.FailErr(t, "capture navigation flow", err)
			}
			if visits.Load() != 1 {
				t.Fatalf("navigation executed %d times, want once", visits.Load())
			}
			if out.FinalURL != server.URL+"/next" || !strings.Contains(string(out.Snapshot), "navigation retained café") {
				t.Fatalf("new document state missing: url=%s snapshot=%s", out.FinalURL, out.Snapshot)
			}
			if len(out.Bytes) == 0 {
				t.Fatal("navigation capture has no visual")
			}
		})
	}
}
