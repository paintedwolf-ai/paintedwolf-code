package page

import (
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestActHandlerRetainsOriginalIndexAndCompletedEffects(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	root := t.TempDir()
	testutil.FailErr(t, "write page fixture", os.WriteFile(filepath.Join(root, "index.html"), []byte(`<button id="save" onclick="this.textContent='Saved'">Save</button>`), 0o600))
	pool := browser.NewPool("")
	pool.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(pool.Close)
	pages := pagesession.NewRegistry(pagesession.DefaultConfig())
	t.Cleanup(func() { pages.Close(t.Context()) })
	held, err := browser.OpenHeld(t.Context(), pool, browser.OpenOpts{ProjectDir: root})
	testutil.FailErr(t, "open test page", err)
	entry, err := pages.Open(t.Context(), "batch", held)
	testutil.FailErr(t, "register test page", err)
	_, err = ActHandler(pages, nil)(t.Context(), map[string]any{
		"id": entry.ID,
		"actions": []any{
			map[string]any{"type": "click", "selector": "#save"},
			map[string]any{"type": "click", "selector": "#missing"},
		},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "batch"},
	})
	var rejection *toolrejection.ToolReject
	if !errors.As(err, &rejection) || rejection.Code != "CAPTURE_ACTION_FAILED" {
		t.Fatalf("tool rejection = %v", err)
	}
	raw, err := json.Marshal(rejection.Data)
	testutil.FailErr(t, "marshal rejection details", err)
	var details struct {
		Index     int `json:"index"`
		Completed []struct {
			Index int `json:"index"`
		} `json:"completed_actions"`
		Controls []struct {
			Text string `json:"text"`
		} `json:"interactive_controls"`
	}
	testutil.FailErr(t, "decode rejection details", json.Unmarshal(raw, &details))
	if details.Index != 1 || len(details.Completed) != 1 || details.Completed[0].Index != 0 {
		t.Fatalf("batch identities = %s", raw)
	}
	if len(details.Controls) != 1 || details.Controls[0].Text != "Saved" {
		t.Fatalf("completed effect missing from current state: %s", raw)
	}
}
