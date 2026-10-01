package browser

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const evidencePage = `<!doctype html>
<body>
<div id="items">loading</div>
<button id="reload" onclick="load()">Reload</button>
<script src="/missing.js"></script>
<script>
  function load() {
    return fetch("/api/items")
      .then((r) => r.ok ? r.json() : Promise.reject(new Error("status " + r.status)))
      .then((items) => { document.getElementById("items").textContent = items.map((i) => i.name).join(","); })
      .catch((e) => { document.getElementById("items").textContent = "error: " + e.message; });
  }
  load();
  console.log("page ready");
  setTimeout(() => { throw new Error("late failure"); }, 0);
</script>
</body>`

func requestTo(network []NetworkRecord, path string) (NetworkRecord, bool) {
	for i := len(network) - 1; i >= 0; i-- {
		if strings.HasSuffix(network[i].URL, path) {
			return network[i], true
		}
	}
	return NetworkRecord{}, false
}

func TestPageEvidenceAndRouteFixturesDescribeWhatThePageRequested(t *testing.T) {
	held := openHeldHTML(t, evidencePage, RouteRule{URL: "/api/items", Body: `[{"name":"alpha"},{"name":"beta"}]`})
	out, err := held.Snapshot(t.Context(), SnapshotOpts{})
	testutil.FailErr(t, "snapshot", err)
	if got := pageValue[string](t, held, `() => document.getElementById("items").textContent`); got != "alpha,beta" {
		t.Fatalf("the routed fixture did not reach the page: %q", got)
	}
	items, ok := requestTo(out.Network, "/api/items")
	if !ok || items.Status != 200 || items.ServedBy != networkServedByRoute || items.Route == nil || *items.Route != 0 || items.Type != "fetch" {
		t.Fatalf("routed request = %+v (all %+v)", items, out.Network)
	}
	missing, ok := requestTo(out.Network, "/missing.js")
	if !ok || missing.Status != 404 || missing.ServedBy != networkServedByProject {
		t.Fatalf("missing script = %+v", missing)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0].Message, "late failure") || !strings.Contains(out.Errors[0].Source, "lycaon.capture") {
		t.Fatalf("errors = %+v", out.Errors)
	}
	if !strings.Contains(strings.Join(out.Log, "\n"), "page ready") {
		t.Fatalf("console = %q", out.Log)
	}

	// Swapping fixtures mid-drive: the next load fails the way a dead backend does.
	report, err := held.Act(t.Context(), []CaptureAction{
		{Type: "route", Routes: []RouteRule{{URL: "/api/items", Fail: "connection_refused"}}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#reload"}},
		{Type: "wait_for", ActionLocator: ActionLocator{Text: "error:"}, TimeoutMS: 5000},
	}, nil)
	testutil.FailErr(t, "reload against a refused backend", err)
	if report.RoutesActive != 1 || len(report.Evidence.Network) != 1 {
		t.Fatalf("drive evidence = %+v, want only the request this drive caused", report.Evidence)
	}
	refused := report.Evidence.Network[0]
	if !strings.Contains(refused.Failure, "CONNECTION_REFUSED") || refused.ServedBy != networkServedByRoute {
		t.Fatalf("refused request = %+v", refused)
	}
	if len(report.Evidence.Errors) != 0 || len(report.Evidence.Log) != 0 {
		t.Fatalf("the drive reported evidence from before it started: %+v", report.Evidence)
	}
}

func TestDelayedAndCountedRoutesShapeTimingAndRetries(t *testing.T) {
	held := openHeldHTML(t, `<!doctype html><button id="go" onclick="fetch('/api/slow').then(r => document.title = 'status ' + r.status)">Go</button>`)
	report, err := held.Act(t.Context(), []CaptureAction{
		{Type: "route", Routes: []RouteRule{{URL: "/api/slow", Status: 503, Times: 1}, {URL: "/api/*", Body: "{}", DelayMS: 400}}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#go"}},
		{Type: "wait_for", ActionLocator: ActionLocator{Text: "Go"}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#go"}},
	}, nil)
	testutil.FailErr(t, "drive", err)
	if len(report.Evidence.Network) != 2 {
		t.Fatalf("network = %+v", report.Evidence.Network)
	}
	first, second := report.Evidence.Network[0], report.Evidence.Network[1]
	if first.Status != 503 || *first.Route != 0 {
		t.Fatalf("first request = %+v, want the one-time 503", first)
	}
	if second.Status != 200 || *second.Route != 1 || second.DurationMS < 350 {
		t.Fatalf("second request = %+v, want the delayed fixture", second)
	}
}

func TestEachHeldPageKeepsItsOwnCookiesAndStorage(t *testing.T) {
	page := `<!doctype html><script>document.title = localStorage.getItem("who") || "nobody";</script>`
	pool := drivePool(t)
	first := openHeldHTMLIn(t, pool, page)
	_ = pageValue[any](t, first, `() => { localStorage.setItem("who", "first"); document.cookie = "session=first"; return null; }`)
	second := openHeldHTMLIn(t, pool, page)
	if who := pageValue[string](t, second, `() => [localStorage.getItem("who"), document.cookie].join("|")`); who != "|" {
		t.Fatalf("a second page saw the first page's storage: %q", who)
	}
}
