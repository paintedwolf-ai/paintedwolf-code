package browser

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

type actionEffectReport struct {
	Target struct {
		ClassName string `json:"className"`
	} `json:"target"`
	Effect struct {
		DOMChanges int `json:"dom_changes"`
		Changed    []struct {
			Attribute string `json:"attribute"`
			Before    string `json:"before"`
			After     string `json:"after"`
		} `json:"changed"`
		Requests []struct {
			Method string `json:"method"`
			URL    string `json:"url"`
		} `json:"requests"`
		TargetAfter struct {
			ClassName string `json:"className"`
		} `json:"target_after"`
	} `json:"effect"`
}

func decodeEffect(t *testing.T, raw json.RawMessage) actionEffectReport {
	t.Helper()
	var out actionEffectReport
	testutil.FailErr(t, "decode action result", json.Unmarshal(raw, &out))
	return out
}

// A board whose squares select on click and whose selected square posts a
// move only to a square the page considers legal.
const effectBoardPage = `<!doctype html>
<style>body{margin:0} .square{display:inline-block;width:60px;height:60px}</style>
<div id="board">
  <div class="square light" id="own"></div>
  <div class="square dark" id="illegal"></div>
  <div class="square light" id="legal"></div>
</div>
<script>
  let selected = null;
  for (const el of document.querySelectorAll(".square")) {
    el.addEventListener("click", () => {
      if (selected && el.id === "legal") {
        fetch("/move", {method: "POST"}).catch(() => {});
        return;
      }
      if (el.id === "own") { el.classList.add("selected"); selected = el; return; }
      if (selected) { selected.classList.remove("selected"); selected = null; }
    });
  }
</script>`

func TestInputStepsReportWhatThePageDidInResponse(t *testing.T) {
	held := openHeldHTML(t, effectBoardPage, RouteRule{URL: "/move", Status: 204})
	report, err := held.Act(t.Context(), []CaptureAction{
		{Type: "click", ActionLocator: ActionLocator{Selector: "#own"}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#illegal"}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#own"}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#legal"}},
	}, nil)
	testutil.FailErr(t, "drive the board", err)

	selectOwn := decodeEffect(t, report.Results[0])
	if len(selectOwn.Effect.Changed) != 1 || selectOwn.Effect.Changed[0].After != "square light selected" ||
		selectOwn.Effect.TargetAfter.ClassName != "square light selected" {
		t.Fatalf("selecting a piece = %+v", selectOwn.Effect)
	}
	// The ignored move deselects and sends nothing: the report says so plainly.
	illegal := decodeEffect(t, report.Results[1])
	if len(illegal.Effect.Requests) != 0 || len(illegal.Effect.Changed) != 1 || illegal.Effect.Changed[0].Before != "square light selected" {
		t.Fatalf("an illegal target = %+v", illegal.Effect)
	}
	legal := decodeEffect(t, report.Results[3])
	if len(legal.Effect.Requests) != 1 || legal.Effect.Requests[0].Method != "POST" {
		t.Fatalf("a legal move = %+v", legal.Effect)
	}
}

func TestAStepWithAFieldItsTypeDoesNotReadIsRefusedBeforeAnyStepRuns(t *testing.T) {
	held := openHeldHTML(t, `<!doctype html><button id="b" onclick="window.clicked=true">Go</button>`)
	_, err := held.Act(t.Context(), []CaptureAction{
		{Type: "click", ActionLocator: ActionLocator{Selector: "#b"}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#b"}, By: &ActionOffset{X: 10, Y: 10}},
	}, nil)
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FIELDS_UNUSED" || rej.Data["index"] != "1" || rej.Data["unused_fields"] != "by" {
		t.Fatalf("click with by = %v", err)
	}
	if clicked := pageValue[any](t, held, `() => window.clicked === true`); clicked != false {
		t.Fatal("a refused batch ran its first step")
	}
}
