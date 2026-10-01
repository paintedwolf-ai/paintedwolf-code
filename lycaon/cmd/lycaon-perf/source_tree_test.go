package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceTreeMeasurementRequiresCompleteFixture(t *testing.T) {
	row := func(path string) api.SourceTreeRow {
		return api.SourceTreeRow{Kind: "directory", Expanded: true, Address: api.SourceTreeAddress{RootID: "root", Path: path}}
	}
	for _, tc := range []struct {
		name  string
		rows  []api.SourceTreeRow
		end   int64
		valid bool
	}{
		{"empty", nil, 0, false},
		{"missing directory", []api.SourceTreeRow{row(".")}, 1, false},
		{"repeated directory", []api.SourceTreeRow{row("pkg0000"), row("pkg0000")}, 2, false},
		{"nonadvancing frame", []api.SourceTreeRow{row(".")}, 0, false},
		{"loading directory", []api.SourceTreeRow{row("pkg0000"), {Kind: "loading"}}, 2, false},
		{"failed directory", []api.SourceTreeRow{row("pkg0000"), {Kind: "error", Error: "listing failed"}}, 2, false},
		{"collapsed directory", []api.SourceTreeRow{{Kind: "directory", Address: api.SourceTreeAddress{Path: "pkg0000"}}}, 1, false},
		{"complete", []api.SourceTreeRow{row("."), row("pkg0000")}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			released := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var result any
				switch {
				case r.URL.Path == "/v1/projects/fixture/source/workspace":
					result = api.SourceWorkspace{WorkspaceID: "workspace", Roots: []api.SourceWorkspaceRoot{{ID: "root"}}}
				case r.Method == http.MethodDelete:
					released = true
					w.WriteHeader(http.StatusNoContent)
					return
				case r.URL.Query().Get("limit") != "":
					result = api.SourceTreeFrame{Kind: "tree", ViewID: "view", ProjectionRevision: "revision", Span: api.SourceViewSpan{End: tc.end}, Rows: tc.rows}
				default:
					result = api.SourceTreeView{Kind: "tree", ID: "view", State: "ready", ProjectionRevision: "revision", Extent: api.SourceViewExtent{Rows: int64(len(tc.rows)), Complete: true}}
				}
				testutil.FailErr(t, "encode source view", json.NewEncoder(w).Encode(result))
			}))
			defer server.Close()
			state := runState{client: &sidecarClient{base: server.URL, http: server.Client()}, project: api.Project{ID: "fixture"}, fixture: fixtureShape{Directories: 1}, metrics: newMeasurements()}
			err := state.measureSourceTree(t.Context())
			if (err == nil) != tc.valid {
				t.Fatalf("measurement error=%v, valid=%v", err, tc.valid)
			}
			if !released {
				t.Fatal("measurement retained its view")
			}
		})
	}
}
