package surveyreceipt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

func TestAttachAndParseJSONArray(t *testing.T) {
	raw := `[{"path":"a.go","line":1}]`
	out := surveyreceipt.Attach(raw, surveyreceipt.New("grep", ".", 1, len(raw), false))
	r, ok := surveyreceipt.Parse(out)
	if !ok || r.Tool != "grep" || r.PathsTouched != 1 {
		t.Fatalf("receipt = %+v ok=%v out=%s", r, ok, out)
	}
}

func TestAttachPlainText(t *testing.T) {
	out := surveyreceipt.Attach("hello", surveyreceipt.New("read", "foo.go", 1, 5, false))
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if payload["content"] != "hello" {
		t.Fatalf("payload = %v", payload)
	}
	if !strings.Contains(out, `"tool":"read"`) {
		t.Fatalf("missing receipt: %s", out)
	}
}
