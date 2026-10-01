package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectEditJSONPreservesExplicitEmptyValues(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{}`, `{"starred":false}`, `{"name":"","starred":false}`} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			var edit api.UpdateProjectRequest
			testutil.FailErr(t, "decode project edit", json.Unmarshal([]byte(input), &edit))
			output, err := json.Marshal(edit)
			testutil.FailErr(t, "encode project edit", err)
			if string(output) != input {
				t.Fatalf("got %s, want %s", output, input)
			}
		})
	}
	var root api.CreateProjectRootInput
	testutil.FailErr(t, "decode explicit secondary root", json.Unmarshal([]byte(`{"path":"/repo","is_primary":false}`), &root))
	if root.IsPrimary == nil || *root.IsPrimary {
		t.Fatal("explicit secondary root lost its presence")
	}
}

func TestAttentionJSONPreservesTimestamp(t *testing.T) {
	t.Parallel()
	var row api.AttentionRow
	testutil.FailErr(t, "decode attention", json.Unmarshal([]byte(`{"session_id":"s","project_id":"p","class":"quiet","reason":"idle","since_at":"2026-09-10T12:13:14.123Z"}`), &row))
	want := time.Date(2026, time.September, 10, 12, 13, 14, 123000000, time.UTC)
	if !row.SinceAt.Equal(want) {
		t.Fatalf("got %v, want %v", row.SinceAt, want)
	}
	encoded, err := json.Marshal(row)
	testutil.FailErr(t, "encode attention", err)
	var fields map[string]json.RawMessage
	testutil.FailErr(t, "inspect attention JSON", json.Unmarshal(encoded, &fields))
	if string(fields["since_at"]) != `"2026-09-10T12:13:14.123Z"` {
		t.Fatalf("timestamp changed: %s", fields["since_at"])
	}
}

func TestBriefingJSONRetainsRequiredEmptyCollections(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(api.FileBriefingResponse{Locations: []api.FileBriefingLocation{}, Sections: []api.FileBriefingSection{}})
	testutil.FailErr(t, "encode empty briefing", err)
	var fields map[string]json.RawMessage
	testutil.FailErr(t, "inspect briefing JSON", json.Unmarshal(encoded, &fields))
	for _, name := range []string{"locations", "sections"} {
		if string(fields[name]) != "[]" {
			t.Fatalf("%s must remain an explicit empty collection, got %s", name, fields[name])
		}
	}
}
