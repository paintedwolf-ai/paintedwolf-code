package literal

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestFileReadsPinExactBytesBeforeOutput: every file a utility reads is
// captured as exact bytes and recorded, in order, under its execution.
func TestFileReadsPinExactBytesBeforeOutput(t *testing.T) {
	h := formHarness(t)
	r := h.exec("", "cat", "crlf", "empty", "bin-a")
	r.expect(t, "cat", "one\r\ntwo\r\n\x00\x01", "", 0)
	pins := h.pins.pins[r.execution]
	if len(pins) != 3 {
		t.Fatalf("pins = %+v", pins)
	}
	for i, want := range []struct{ path, content string }{{"crlf", "one\r\ntwo\r\n"}, {"empty", ""}, {"bin-a", "\x00\x01"}} {
		sum := textfile.SHA256([]byte(want.content))
		got := pins[i]
		if got.Role != toolcontract.SourceRevision || got.Mode != toolcontract.PublishedDiskRevision || got.Basis != toolcontract.CapturedRevision ||
			got.RootID != "r1" || got.Path != want.path || got.Representation != toolcontract.OriginalEncodedBytes ||
			got.DiskSHA256 != sum || got.ContentSHA256 != sum || got.ContentBytes != int64(len(want.content)) {
			t.Fatalf("pin %d = %+v", i, got)
		}
	}
	stdin := h.exec("piped", "sha256sum")
	if len(h.pins.pins[stdin.execution]) != 0 {
		t.Fatalf("stdin read recorded a file pin: %+v", h.pins.pins[stdin.execution])
	}
	missing := h.exec("", "cat", "missing")
	if len(h.pins.pins[missing.execution]) != 0 {
		t.Fatalf("missing file pinned: %+v", h.pins.pins[missing.execution])
	}
}

// TestPinRecordingIsRequiredBeforeBytes: without a recorder a read refuses
// before emitting anything.
func TestPinRecordingIsRequiredBeforeBytes(t *testing.T) {
	h := formHarness(t)
	h.owners.Pins = nil
	r := h.exec("", "cat", "input")
	if r.err == nil || r.stdout != "" || r.result.Terminal != toolcontract.ProducerFailed || r.result.PublicationEligible {
		t.Fatalf("unpinned read = %+v %v %q", r.result, r.err, r.stdout)
	}
}

// TestLiteralWindowsMapExactSourceRanges: literal windows and reversible
// numbering carry exact mappings; transformed or stdin bytes carry none.
func TestLiteralWindowsMapExactSourceRanges(t *testing.T) {
	h := formHarness(t)
	type span struct{ src, out [2]int64 }
	cases := []struct {
		argv  string
		spans []span
	}{
		{"cat input", []span{{[2]int64{0, 10}, [2]int64{0, 10}}}},
		{"head -n 1 input", []span{{[2]int64{0, 6}, [2]int64{0, 6}}}},
		{"tail -n 1 input", []span{{[2]int64{6, 10}, [2]int64{0, 4}}}},
		{"tail -c +3 input", []span{{[2]int64{2, 10}, [2]int64{0, 8}}}},
		{"head -c 3 input", []span{{[2]int64{0, 3}, [2]int64{0, 3}}}},
		{"nl -ba -w 1 -s : input", []span{{[2]int64{0, 6}, [2]int64{2, 8}}, {[2]int64{6, 10}, [2]int64{10, 14}}}},
		{"cat -n input", []span{{[2]int64{0, 6}, [2]int64{7, 13}}, {[2]int64{6, 10}, [2]int64{20, 24}}}},
		{"cat -A input", nil},
		{"cat -v input", nil},
		{"base64 input", nil},
		{"cat -", nil},
	}
	for _, c := range cases {
		t.Run(c.argv, func(t *testing.T) {
			r := h.exec("alpha\nbeta", strings.Fields(c.argv)...)
			if r.err != nil || len(r.mappings) != len(c.spans) {
				t.Fatalf("mappings = %+v err %v", r.mappings, r.err)
			}
			pins := h.pins.pins[r.execution]
			for i, want := range c.spans {
				m := r.mappings[i]
				pin, _ := pins[0].SourcePin()
				if m.Source != pin || m.SourceStartByte != want.src[0] || m.SourceEndByte != want.src[1] || m.OutputStartByte != want.out[0] || m.OutputEndByte != want.out[1] {
					t.Fatalf("mapping %d = %+v, want %+v", i, m, want)
				}
				if got := r.stdout[m.OutputStartByte:m.OutputEndByte]; got != "alpha\nbeta"[m.SourceStartByte:m.SourceEndByte] {
					t.Fatalf("mapped output %q differs from its source", got)
				}
			}
		})
	}
}

// TestComparisonPublicationIsDiffOnly: diff's complete status 1 publishes
// as a comparison; other ordinary failures never do.
func TestComparisonPublicationIsDiffOnly(t *testing.T) {
	h := formHarness(t)
	diff := h.exec("", "diff", "old", "new")
	if diff.result.Status != 1 || !diff.result.Complete || !diff.result.PublicationEligible || diff.result.PublicationBasis != toolcontract.NativeComparisonPublication {
		t.Fatalf("diff status 1 = %+v", diff.result)
	}
	same := h.exec("", "diff", "same-a", "same-b")
	if !same.result.PublicationEligible || same.result.PublicationBasis != toolcontract.ZeroStatusPublication {
		t.Fatalf("equal diff = %+v", same.result)
	}
	trouble := h.exec("", "diff", "missing", "input")
	if trouble.result.Status != 2 || trouble.result.PublicationEligible {
		t.Fatalf("diff trouble = %+v", trouble.result)
	}
	for _, argv := range [][]string{{"cat", "missing"}, {"base64", "-d", "bin-a"}} {
		r := h.exec("", argv...)
		if r.result.Status != 1 || !r.result.Complete || r.result.PublicationEligible || r.result.Terminal != toolcontract.ProducerCompleted {
			t.Fatalf("%v = %+v", argv, r.result)
		}
	}
}

func TestCancellationInterrupts(t *testing.T) {
	h := formHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	prepared, err := h.owners.Prepare(mustPaths(t, h), requestFor(h, "cat", "input"))
	testutil.FailErr(t, "prepare native literal command", err)
	result, err := prepared.InvokeOwner(ctx, h.tc, streamsFor(""))
	if err == nil || result.Terminal != toolcontract.ProducerInterrupted || result.PublicationEligible {
		t.Fatalf("canceled = %+v %v", result, err)
	}
}
