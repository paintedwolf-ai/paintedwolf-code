package browser

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestActionFailedDataPresentsLocatorsAndInventory(t *testing.T) {
	res, err := json.Marshal(map[string]any{
		"ok":    false,
		"error": "target not found",
		"state": map[string]any{
			"interactive": []any{
				map[string]any{"tag": "button", "name": "Check configuration"},
				map[string]any{"tag": "a", "text": "Docs"},
			},
		},
	})
	testutil.FailErr(t, "marshal", err)
	data := actionFailedData(0, CaptureAction{
		Type:          "click",
		ActionLocator: ActionLocator{Role: "button", Label: "Check configuration"},
	}, res, nil)
	if data["index"] != 0 || data["type"] != "click" {
		t.Fatalf("identity = %#v", data)
	}
	if data["error"] != "target not found" {
		t.Fatalf("error = %#v", data["error"])
	}
	locs, _ := data["locators"].(string)
	if !strings.Contains(locs, "role=button") || !strings.Contains(locs, "Check configuration") {
		t.Fatalf("locators = %q", locs)
	}
	interactive, _ := data["interactive"].(string)
	if !strings.Contains(interactive, `button "Check configuration"`) || !strings.Contains(interactive, `a "Docs"`) {
		t.Fatalf("interactive = %q", interactive)
	}
	if _, ok := data["state"]; ok {
		t.Fatal("raw state must not be copied onto reject data")
	}
}

func TestActionFailedDataPrefersDriverErrorOverRunError(t *testing.T) {
	res, err := json.Marshal(map[string]any{"ok": false, "error": "control not enabled"})
	testutil.FailErr(t, "marshal", err)
	data := actionFailedData(1, CaptureAction{Type: "click", ActionLocator: ActionLocator{Testid: "save"}}, res, errors.New("cdp timeout"))
	if data["error"] != "control not enabled" {
		t.Fatalf("error = %#v", data["error"])
	}
	if data["locators"] != "testid=save" {
		t.Fatalf("locators = %#v", data["locators"])
	}
}

func TestDriverOK(t *testing.T) {
	if !driverOK(nil) {
		t.Fatal("missing body is ok")
	}
	ok, err := json.Marshal(map[string]any{"ok": true})
	testutil.FailErr(t, "marshal ok", err)
	if !driverOK(ok) {
		t.Fatal("ok:true")
	}
	fail, err := json.Marshal(map[string]any{"ok": false})
	testutil.FailErr(t, "marshal fail", err)
	if driverOK(fail) {
		t.Fatal("ok:false")
	}
}

func TestLocatorSummaryEmptyWhenUntargeted(t *testing.T) {
	if got := locatorSummary(CaptureAction{Type: "wait", Wait: "idle"}); got != "" {
		t.Fatalf("locatorSummary = %q", got)
	}
}
