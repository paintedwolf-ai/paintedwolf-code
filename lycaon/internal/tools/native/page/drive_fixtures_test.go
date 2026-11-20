package page

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// A route body_path is a session-folder path: @label reads a file in any
// attached root, and an unknown label is refused as a route outside the
// workspace, for page routes and for route actions alike.
func TestDriveFixturesResolveRouteBodiesAcrossRoots(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "lib")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write body", os.WriteFile(filepath.Join(secondary, "body.json"), []byte(`{"from":"lib"}`), 0o600))
	tctx := tools.ToolContext{Source: tools.InvocationSource{
		Roots: []projectroot.RootRef{
			{ID: "p", Label: "app", Path: primary, IsPrimary: true},
			{ID: "s", Label: "lib", Path: secondary},
		},
		ActiveRootID: "p",
	}}
	route := func(path string) []browser.RouteRule {
		return []browser.RouteRule{{URL: "/api", Status: 200, BodyPath: path}}
	}
	action := func(path string) []browser.CaptureAction {
		return []browser.CaptureAction{{Type: "route", Routes: route(path)}}
	}

	pageRoutes, actionRoutes := route("@lib/body.json"), action("@lib/body.json")
	testutil.FailErr(t, "resolve labelled bodies", resolveDriveFixtures(t.Context(), tctx, pageRoutes, actionRoutes))
	for what, got := range map[string][]byte{"page route": pageRoutes[0].BodyBytes, "route action": actionRoutes[0].Routes[0].BodyBytes} {
		if string(got) != `{"from":"lib"}` {
			t.Fatalf("%s body = %q, want the lib root's file", what, got)
		}
	}

	for what, err := range map[string]error{
		"page route":   resolveDriveFixtures(t.Context(), tctx, route("@missing/body.json"), nil),
		"route action": resolveDriveFixtures(t.Context(), tctx, nil, action("@missing/body.json")),
	} {
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "CAPTURE_ROUTE_INVALID" || reject.Data["reason"] != "body_path_outside_workspace" {
			t.Fatalf("%s with an unknown label = %v, want CAPTURE_ROUTE_INVALID outside the workspace", what, err)
		}
	}
}
