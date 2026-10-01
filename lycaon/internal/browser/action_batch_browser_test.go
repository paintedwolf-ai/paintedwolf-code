package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrowserBatchFailureAcrossCaptureModes(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	t.Cleanup(pool.Close)
	for _, mode := range []string{"held", "preview", CaptureModeScreenshot, CaptureModeFilmstrip} {
		t.Run(mode, func(t *testing.T) {
			actions := []CaptureAction{
				{Type: "click", ActionLocator: ActionLocator{Selector: "#check"}, Wait: "idle"},
				{Type: "click", ActionLocator: ActionLocator{Selector: "#missing"}},
				{Type: "click", ActionLocator: ActionLocator{Selector: "#unreached"}},
			}
			observed := 0
			observe := func(CaptureAction, json.RawMessage) { observed++ }
			var err error
			if mode == "held" {
				held, openErr := OpenHeld(t.Context(), pool, OpenOpts{ProjectDir: fixtureDir(t, "role-label-button")})
				testutil.FailErr(t, "open batch page", openErr)
				t.Cleanup(func() { _ = held.Close(t.Context()) })
				_, err = held.Act(t.Context(), actions, observe)
				out, snapshotErr := held.Snapshot(t.Context(), SnapshotOpts{})
				testutil.FailErr(t, "snapshot partial effects", snapshotErr)
				if !strings.Contains(string(out.Snapshot), "ok:static") {
					t.Fatalf("completed effect missing: %s", out.Snapshot)
				}
			} else {
				req := CaptureRequest{ProjectDir: fixtureDir(t, "role-label-button"), Actions: actions, Width: 400, Height: 300}
				if mode == "preview" {
					req.Preview = &CapturePreview{Action: observe}
				} else {
					req.Mode = mode
				}
				_, err = (&CapturePool{Pool: pool}).Capture(t.Context(), req)
			}
			var rej *browserengine.RejectError
			if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FAILED" || rej.Data["index"] != 1 {
				t.Fatalf("batch failure identity = %v", err)
			}
			if got := rej.Data["completed_actions"].([]completedAction); len(got) != 1 || got[0].Locators != "selector=#check" {
				t.Fatalf("completed prefix = %+v", got)
			}
			if (mode == "held" || mode == "preview") && observed != 1 {
				t.Fatalf("preview observed %d steps, want 1", observed)
			}
		})
	}
}

func TestActionResultContainsSettledControlState(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	t.Cleanup(pool.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<button id="save" onclick="setTimeout(() => { this.disabled = true; this.textContent = 'Saved'; }, 50)">Save</button>`)
	}))
	t.Cleanup(server.Close)
	for _, wait := range []string{"", "idle"} {
		t.Run("wait="+wait, func(t *testing.T) {
			held, err := OpenHeld(t.Context(), pool, OpenOpts{URL: server.URL})
			testutil.FailErr(t, "open settling page", err)
			t.Cleanup(func() { _ = held.Close(t.Context()) })
			report, err := held.Act(t.Context(), []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#save"}, Wait: wait}}, nil)
			testutil.FailErr(t, "click asynchronous save", err)
			var result struct {
				State struct {
					Interactive []actionControl `json:"interactive"`
				} `json:"state"`
			}
			testutil.FailErr(t, "decode settled result", json.Unmarshal(report.Results[0], &result))
			controls := result.State.Interactive
			if len(controls) != 1 || controls[0].Disabled == nil || !*controls[0].Disabled || controls[0].Text != "Saved" {
				t.Fatalf("result returned pre-settlement state: %+v", controls)
			}
		})
	}
}
