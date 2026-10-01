package visual

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStampArtifactID_jsonLeadingKey(t *testing.T) {
	out := StampArtifactID(`{"mime":"image/png","caption":"A"}`, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if !strings.HasPrefix(out, `{"artifact_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",`) {
		t.Fatalf("want artifact_id first, got %s", out)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if root["artifact_id"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || root["mime"] != "image/png" {
		t.Fatalf("root = %#v", root)
	}
}

func TestStampArtifactID_afterHandlePrefix(t *testing.T) {
	in := "[render#1]\n{\"mime\":\"image/png\",\"width\":10}"
	out := StampArtifactID(in, "id-1")
	if !strings.HasPrefix(out, "[render#1]\n{\"artifact_id\":\"id-1\",") {
		t.Fatalf("got %s", out)
	}
}

func TestStampArtifactID_preservesSuffixAfterQuotedBrace(t *testing.T) {
	in := "[render#1]\n{\"mime\":\"image/png\",\"caption\":\"literal } brace\"}\n>>> feedback {not-json}"
	out := StampArtifactID(in, "id-1")
	if !strings.HasSuffix(out, "\n>>> feedback {not-json}") {
		t.Fatalf("suffix was not preserved: %q", out)
	}
	_, body, _, ok := hostmarker.SplitToolJSONBody(out)
	if !ok {
		t.Fatalf("stamped output is not structured JSON: %q", out)
	}
	var root map[string]any
	testutil.FailErr(t, "unmarshal stamped body", json.Unmarshal([]byte(body), &root))
	if root["caption"] != "literal } brace" || root["artifact_id"] != "id-1" {
		t.Fatalf("stamped body = %#v", root)
	}
}

func TestStampArtifactID_upsertReplaces(t *testing.T) {
	in := `{"artifact_id":"old","mime":"image/png"}`
	out := StampArtifactID(in, "new-id")
	if strings.Contains(out, `"old"`) || !strings.Contains(out, `"artifact_id":"new-id"`) {
		t.Fatalf("got %s", out)
	}
	if strings.Count(out, `"artifact_id"`) != 1 {
		t.Fatalf("duplicate keys: %s", out)
	}
}

func TestStampArtifactID_nonJSON(t *testing.T) {
	out := StampArtifactID("plain text result", "id-2")
	if out != "artifact_id: id-2\nplain text result" {
		t.Fatalf("got %q", out)
	}
}
