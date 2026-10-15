package linuxsandbox

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRefusalLinesWriteOneJSONLineEach(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lines := &refusalLines{w: &buf}
	lines.report(Refusal{Operation: "network-outbound", Target: "remote:192.0.2.1:53", Process: "dig"})
	lines.report(Refusal{Unexamined: true})
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("wrote %q, want two lines", buf.String())
	}
	var first Refusal
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(got[0]), &first))
	if first.Target != "remote:192.0.2.1:53" || first.Process != "dig" || first.Unexamined {
		t.Fatalf("decoded %+v", first)
	}
	if got[1] != `{"unexamined":true}` {
		t.Fatalf("unexamined line %q", got[1])
	}
	// No feed, or no reporter at all, writes nothing and does not fail.
	(&refusalLines{}).report(first)
	var none *refusalLines
	none.report(first)
}
