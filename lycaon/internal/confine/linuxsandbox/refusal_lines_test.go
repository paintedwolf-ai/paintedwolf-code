package linuxsandbox

import (
	"bytes"
	"encoding/json"
	"strconv"
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

func TestRefusalLinesBoundsRepeatedAndDistinctReports(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lines := &refusalLines{w: &buf}
	for range maxLaunchRepeatsPerKey + 50 {
		lines.report(Refusal{Operation: "file-read-data", Target: "/tmp/repeated", Process: "cat"})
	}
	if got := strings.Count(buf.String(), "\n"); got != maxLaunchRepeatsPerKey {
		t.Fatalf("repeated reports wrote %d lines, want %d", got, maxLaunchRepeatsPerKey)
	}
	buf.Reset()
	lines = &refusalLines{w: &buf}
	for i := range maxLaunchDistinctRefusals + 25 {
		lines.report(Refusal{Operation: "file-read-data", Target: "/tmp/f-" + strconv.Itoa(i), Process: "cat"})
	}
	written := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(written) != maxLaunchDistinctRefusals+1 {
		t.Fatalf("distinct overflow wrote %d lines, want %d", len(written), maxLaunchDistinctRefusals+1)
	}
	if written[len(written)-1] != `{"unexamined":true}` {
		t.Fatalf("overflow marker = %q, want unexamined marker", written[len(written)-1])
	}
}
