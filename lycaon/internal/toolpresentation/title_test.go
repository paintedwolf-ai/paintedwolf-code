package toolpresentation

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogTitles(t *testing.T) {
	body, err := os.ReadFile("testdata/titles.json")
	testutil.FailErr(t, "read title cases", err)
	var cases []struct {
		Tool  string
		Args  map[string]any
		Title string
	}
	testutil.FailErr(t, "decode title cases", json.Unmarshal(body, &cases))
	for _, tc := range cases {
		t.Run(tc.Tool, func(t *testing.T) {
			if got := Title(tc.Tool, tc.Args); got != tc.Title {
				t.Fatalf("title = %q, want %q", got, tc.Title)
			}
		})
	}
}

func TestSubjectTitles(t *testing.T) {
	for _, tc := range []struct {
		tool          string
		args          map[string]any
		subject, want string
	}{
		{"command_output", map[string]any{"handle": "opaque"}, "./task den:test", "./task den:test"},
		{"worker_cancel", map[string]any{"job_id": "opaque", "reason": "Superseded"}, "Fix login", "Fix login · Superseded"},
		{"preview_overlay", map[string]any{"overlay_id": "opaque", "path": "app.ts"}, "Fix login", "Fix login · app.ts"},
		{"held_result", nil, "fetch url · https://example.com", "fetch url · https://example.com"},
	} {
		if got := SubjectTitle(tc.tool, tc.args, tc.subject); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.tool, got, tc.want)
		}
	}
}
