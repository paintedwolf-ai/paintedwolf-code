package browser

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

// drivePool is a browser pool for drive tests, skipped where no browser is provisioned.
func drivePool(t *testing.T) *Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("browser drive")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	t.Cleanup(pool.Close)
	return pool
}

// openHeldHTML serves one page from a temporary project tree and holds it open.
func openHeldHTML(t *testing.T, html string, routes ...RouteRule) *HeldPage {
	t.Helper()
	return openHeldHTMLIn(t, drivePool(t), html, routes...)
}

func openHeldHTMLIn(t *testing.T, pool *Pool, html string, routes ...RouteRule) *HeldPage {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "write page", os.WriteFile(filepath.Join(root, "index.html"), []byte(html), 0o600))
	held, err := OpenHeld(t.Context(), pool, OpenOpts{ProjectDir: root, Width: 800, Height: 600, Routes: routes})
	testutil.FailErr(t, "open held page", err)
	t.Cleanup(func() { _ = held.Close(t.Context()) })
	return held
}

// pageValue evaluates an expression in the held page and decodes its JSON value.
func pageValue[T any](t *testing.T, held *HeldPage, expr string) T {
	t.Helper()
	res, err := held.Page.Context(t.Context()).Eval(expr)
	testutil.FailErr(t, "evaluate "+expr, err)
	var out T
	raw, err := res.Value.MarshalJSON()
	testutil.FailErr(t, "encode page value", err)
	testutil.FailErr(t, "decode page value", json.Unmarshal(raw, &out))
	return out
}

const inputPage = `<!doctype html>
<style>body{margin:0;font:16px sans-serif} #hover:hover{background:rgb(0,128,0)} .box{height:40px}</style>
<script>
  window.events = [];
  const note = (e, extra) => events.push(Object.assign({type: e.type, trusted: e.isTrusted, target: e.target.id}, extra || {}));
</script>
<button id="save" class="box" onclick="note(event, {detail: event.detail, y: event.clientY})">Save</button>
<div id="hover" class="box" onmouseenter="note(event)" onmouseleave="note(event)">Hover me</div>
<input id="name" value="old value" onkeydown="note(event, {key: event.key})" oninput="note(event, {value: this.value})">
<select id="plan" onchange="note(event, {value: this.value})"><option value="free">Free</option><option value="pro">Pro plan</option></select>
<div id="list" style="height:120px;overflow:auto" onwheel="note(event, {dy: event.deltaY})"><div style="height:2000px">rows</div></div>
<button id="dbl" class="box" ondblclick="note(event)" oncontextmenu="note(event, {button: event.button}); return false">Double</button>
<script>document.addEventListener("keydown", (e) => { if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) note(e, {key: e.key, meta: e.metaKey, ctrl: e.ctrlKey}); });</script>`

type pageEvent struct {
	Type    string  `json:"type"`
	Trusted bool    `json:"trusted"`
	Target  string  `json:"target"`
	Detail  int     `json:"detail"`
	Key     string  `json:"key"`
	Value   string  `json:"value"`
	DY      float64 `json:"dy"`
	Meta    bool    `json:"meta"`
	Ctrl    bool    `json:"ctrl"`
	Button  int     `json:"button"`
}

func eventsOf(events []pageEvent, typ string) []pageEvent {
	var out []pageEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestDriveSendsTrustedPointerAndKeyboardInput(t *testing.T) {
	held := openHeldHTML(t, inputPage)
	report, err := held.Act(t.Context(), []CaptureAction{
		{Type: "click", ActionLocator: ActionLocator{Selector: "#save"}},
		{Type: "hover", ActionLocator: ActionLocator{Selector: "#hover"}, HoldMS: 50},
		{Type: "fill", ActionLocator: ActionLocator{Selector: "#name"}, Value: "Ada"},
		{Type: "type", Value: " L"},
		{Type: "select", ActionLocator: ActionLocator{Selector: "#plan"}, Value: "Pro plan"},
		{Type: "scroll", ActionLocator: ActionLocator{Selector: "#list"}, By: &ActionOffset{Y: 300}},
		{Type: "press", Key: "Mod+K"},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#dbl"}, Count: 2},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#dbl"}, Button: "right"},
	}, nil)
	testutil.FailErr(t, "drive", err)
	events := pageValue[[]pageEvent](t, held, `() => window.events`)
	for _, e := range events {
		if e.Type != "change" && e.Type != "input" && !e.Trusted {
			t.Fatalf("%s on #%s was synthetic, not browser input: %+v", e.Type, e.Target, events)
		}
	}
	if clicks := eventsOf(events, "click"); len(clicks) != 1 || clicks[0].Detail != 1 {
		t.Fatalf("clicks = %+v", clicks)
	}
	if enters := eventsOf(events, "mouseenter"); len(enters) != 1 {
		t.Fatalf("hover did not enter #hover: %+v", events)
	}
	if inputs := eventsOf(events, "input"); len(inputs) == 0 || inputs[len(inputs)-1].Value != "Ada L" {
		t.Fatalf("fill then type left %+v", inputs)
	}
	if keys := eventsOf(events, "keydown"); len(keys) < 2 {
		t.Fatalf("typing sent no key presses: %+v", keys)
	}
	if changes := eventsOf(events, "change"); len(changes) == 0 || changes[len(changes)-1].Value != "pro" {
		t.Fatalf("select changes = %+v", changes)
	}
	if wheels := eventsOf(events, "wheel"); len(wheels) < 3 {
		t.Fatalf("scroll sent %d wheel notches", len(wheels))
	}
	if top := pageValue[float64](t, held, `() => document.getElementById("list").scrollTop`); top < 250 {
		t.Fatalf("list scrolled to %v, want about 300", top)
	}
	var chord []pageEvent
	for _, e := range eventsOf(events, "keydown") {
		if e.Key == "k" || e.Key == "K" {
			chord = append(chord, e)
		}
	}
	if len(chord) != 1 || (runtime.GOOS == "darwin" && !chord[0].Meta) || (runtime.GOOS != "darwin" && !chord[0].Ctrl) {
		t.Fatalf("Mod+K = %+v", chord)
	}
	if len(eventsOf(events, "dblclick")) != 1 {
		t.Fatalf("count 2 did not double click: %+v", events)
	}
	if menus := eventsOf(events, "contextmenu"); len(menus) != 1 || menus[0].Button != 2 {
		t.Fatalf("right click = %+v", menus)
	}
	var clickResult struct {
		Point  struct{ X, Y float64 } `json:"point"`
		Target struct{ Tag, ID string } `json:"target"`
	}
	testutil.FailErr(t, "decode click result", json.Unmarshal(report.Results[0], &clickResult))
	if clickResult.Target.ID != "save" || clickResult.Point.Y <= 0 {
		t.Fatalf("click result = %s", report.Results[0])
	}
}

func TestPointerLandsWhereTheTargetReceivesItOrFailsWhereNoReaderCouldClick(t *testing.T) {
	held := openHeldHTML(t, `<!doctype html>
<style>body{margin:0} button{display:block;width:200px;height:80px}</style>
<script>window.hits = [];</script>
<button id="half" onclick="hits.push(event.clientY)">Half covered</button>
<div style="position:fixed;top:0;left:0;width:300px;height:50px;background:#333">sticky header</div>
<button id="covered" style="margin-top:40px" onclick="hits.push('covered')">Covered</button>
<div id="scrim" style="position:fixed;top:110px;left:0;width:300px;height:200px;background:rgba(0,0,0,.4)"></div>`)
	_, err := held.Act(t.Context(), []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#half"}}}, nil)
	testutil.FailErr(t, "click the uncovered part of #half", err)
	if hits := pageValue[[]any](t, held, `() => window.hits`); len(hits) != 1 || hits[0].(float64) < 50 {
		t.Fatalf("click landed under the header: %v", hits)
	}
	_, err = held.Act(t.Context(), []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#covered"}, TimeoutMS: 300}}, nil)
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FAILED" || rej.Data["error"] != "target obscured" {
		t.Fatalf("clicking a covered button = %v", err)
	}
	if hits := pageValue[[]any](t, held, `() => window.hits`); len(hits) != 1 {
		t.Fatalf("a covered button received a click: %v", hits)
	}
}

func TestDragMovesThroughPointerEventsToItsDropTarget(t *testing.T) {
	held := openHeldHTML(t, `<!doctype html>
<style>body{margin:0} #card{position:absolute;left:20px;top:20px;width:80px;height:40px;background:#48c;touch-action:none}
#zone{position:absolute;left:400px;top:300px;width:150px;height:100px;border:2px dashed #888}</style>
<div id="card">card</div><div id="zone">drop here</div>
<script>
  window.drag = {moves: 0, dropped: null};
  const card = document.getElementById("card");
  let from = null;
  card.addEventListener("pointerdown", (e) => { from = {x: e.clientX - card.offsetLeft, y: e.clientY - card.offsetTop}; card.setPointerCapture(e.pointerId); });
  card.addEventListener("pointermove", (e) => { if (!from) return; drag.moves++; card.style.left = (e.clientX - from.x) + "px"; card.style.top = (e.clientY - from.y) + "px"; });
  card.addEventListener("pointerup", (e) => {
    from = null;
    card.style.pointerEvents = "none";
    drag.dropped = document.elementFromPoint(e.clientX, e.clientY).id;
    card.style.pointerEvents = "";
  });
</script>`)
	report, err := held.Act(t.Context(), []CaptureAction{{Type: "drag", ActionLocator: ActionLocator{Selector: "#card"}, To: &ActionLocator{Selector: "#zone"}}}, nil)
	testutil.FailErr(t, "drag card to zone", err)
	drag := pageValue[struct {
		Moves   int    `json:"moves"`
		Dropped string `json:"dropped"`
	}](t, held, `() => window.drag`)
	if drag.Dropped != "zone" || drag.Moves < dragSteps/2 {
		t.Fatalf("drag = %+v (result %s)", drag, report.Results[0])
	}
}
