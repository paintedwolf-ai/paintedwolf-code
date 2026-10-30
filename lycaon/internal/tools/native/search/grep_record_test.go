package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func grepMemory(t *testing.T, maximum int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: maximum, State: 8 << 20})
	testutil.FailErr(t, "create grep memory", err)
	return memory
}

func TestGrepRecordJSONPreservesArbitraryBytes(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("plain β text\n"), {0, 1, '\b', '\f', '\r', '\t', '"', '\\'}, {0xff, 0xfe, 0, 'a'}} {
		h := newHarness(t, nil)
		h.owners.Revisions.Memory = grepMemory(t, 1<<20)
		r := parallelFixture(t, h, t.Context(), &bytes.Buffer{})
		buffer := &grepRecord{inv: r.inv}
		testutil.FailErr(t, "encode record bytes", buffer.jsonData(raw))
		var encoded map[string]string
		testutil.FailErr(t, "parse encoded record", json.Unmarshal(buffer.bytes, &encoded))
		decoded := []byte(encoded["text"])
		if binary, ok := encoded["bytes"]; ok {
			var err error
			decoded, err = base64.StdEncoding.DecodeString(binary)
			testutil.FailErr(t, "decode binary record", err)
		}
		if !bytes.Equal(decoded, raw) {
			t.Fatalf("record bytes changed: got %x want %x", decoded, raw)
		}
		buffer.close()
		if h.owners.Revisions.Memory.InUse().Working != 0 {
			t.Fatalf("JSON memory leaked: %+v", h.owners.Revisions.Memory.InUse())
		}
	}
}

func TestGrepRecordGrowthAndCancellationReleaseAdmission(t *testing.T) {
	h := newHarness(t, nil)
	memory := grepMemory(t, 512)
	h.owners.Revisions.Memory = memory
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := parallelFixture(t, h, ctx, &bytes.Buffer{})
	buffer := &grepRecord{inv: r.inv}
	testutil.FailErr(t, "start record", buffer.text(strings.Repeat("a", 256)))
	if err := buffer.text(strings.Repeat("b", 300)); err == nil || len(buffer.bytes) != 256 || memory.InUse().Working != 256 {
		t.Fatalf("unadmitted record growth: err=%v len=%d memory=%+v", err, len(buffer.bytes), memory.InUse())
	}
	cancel()
	if err := buffer.text("c"); !errors.Is(err, context.Canceled) {
		t.Fatalf("record cancellation = %v", err)
	}
	buffer.close()
	if memory.InUse().Working != 0 {
		t.Fatalf("record memory leaked: %+v", memory.InUse())
	}
}

func TestGrepTransformedOutputSharesProgramAdmission(t *testing.T) {
	for _, argv := range [][]string{
		{"rg", "--json", "a", "input"},
		{"rg", "--json", "-r", strings.Repeat("x", 100), "a", "input"},
		{"rg", "-r", strings.Repeat("x", 1000), "a", "input"},
		{"rg", "-o", "-r", strings.Repeat("x", 3<<20), "a", "input"},
	} {
		t.Run(strings.Join(argv[:min(2, len(argv))], " "), func(t *testing.T) {
			h := newHarness(t, map[string]string{"input": strings.Repeat("a", 50000) + "\n"})
			memory := grepMemory(t, 2<<20)
			h.owners.Revisions.Memory = memory
			// Isolate memory growth from the regex work limit.
			h.owners.regex.WorkSteps = 1 << 40
			r := h.exec("", argv...)
			reject := toolrejection.AsToolReject(r.err)
			if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) || memory.Peak().Working > 2<<20 {
				t.Fatalf("transformed output admission: result=%+v err=%v peak=%+v held=%+v", r.result, r.err, memory.Peak(), memory.InUse())
			}
		})
	}
}

func TestGrepWorkerCountAdaptsBeforeReads(t *testing.T) {
	h := newHarness(t, nil)
	memory := grepMemory(t, 2*grepWorkerCredit)
	h.owners.Revisions.Memory = memory
	r := parallelFixture(t, h, t.Context(), &bytes.Buffer{})
	var err error
	r.files, err = newGrepFiles(r, grepWorkerMaximum)
	testutil.FailErr(t, "adapt worker capacity", err)
	if r.files.window != 2 || memory.InUse().Working != grepWorkerCredit {
		t.Fatalf("worker capacity: window=%d memory=%+v", r.files.window, memory.InUse())
	}
	testutil.FailErr(t, "finish adapted workers", r.files.finish(nil))
	if memory.AvailableWorking() != 2*grepWorkerCredit {
		t.Fatalf("available memory after release = %d", memory.AvailableWorking())
	}
}

func TestGrepReplacementGrammarNeverIndexesMalformedGroups(t *testing.T) {
	for _, replacement := range []string{"${-1}", "${+1}", "${}", "${x}", "${1", "$-1", "$x", "${999999999999999999999999999999999999}"} {
		t.Run(replacement, func(t *testing.T) {
			h := newHarness(t, map[string]string{"input": "a\n"})
			h.exec("", "rg", "-r", replacement, "(a)(b)?", "input").expect(t, "literal malformed group", replacement+"\n", "", 0)
		})
	}
	h := newHarness(t, map[string]string{"input": "a\n"})
	h.exec("", "rg", "-r", "$0:${1}:${2}:$$", "(a)(b)?", "input").expect(t, "matched and unmatched groups", "a:a::$\n", "", 0)
	for _, replacement := range []string{"$3", "${3}", "$" + strconv.Itoa(int(^uint(0)>>1)), "${" + strconv.Itoa(int(^uint(0)>>1)) + "}"} {
		r := h.exec("", "rg", "-r", replacement, "(a)(b)?", "input")
		if r.err != nil || r.result.Status != 2 || r.stdout != "" || !strings.Contains(r.stderr, "capture group") {
			t.Fatalf("invalid numbered group %q: result=%+v err=%v stdout=%q stderr=%q", replacement, r.result, r.err, r.stdout, r.stderr)
		}
	}
}

func TestGrepRecordRefusesOutputCapacityBeforeAllocation(t *testing.T) {
	h := newHarness(t, nil)
	memory := grepMemory(t, 1<<20)
	h.owners.Revisions.Memory, h.owners.StreamBytes = memory, 10
	r := parallelFixture(t, h, t.Context(), &bytes.Buffer{})
	buffer := &grepRecord{inv: r.inv}
	err := buffer.text("elevenbytes")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || cap(buffer.bytes) != 0 || memory.InUse().Working != 0 {
		t.Fatalf("record capacity: err=%v capacity=%d held=%+v", err, cap(buffer.bytes), memory.InUse())
	}
	buffer.close()
}
