package mutation

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestMkdirExistingRootContinuesConditionalProgram(t *testing.T) {
	h := newHarness(t, map[string]string{"existing/file.txt": "kept"})
	out, err := h.evaluate("mkdir -p . existing && echo written > result.txt")
	if err != nil || out.Stop != nil || out.Status != 0 || h.read("result.txt") != "written\n" {
		t.Fatalf("directory assurance failed: outcome=%+v err=%v", out, err)
	}
	out, err = h.evaluate("mkdir -p existing/file.txt && echo wrong > skipped.txt")
	if err != nil || out.Status == 0 || !h.absent("skipped.txt") {
		t.Fatalf("file collision continued: outcome=%+v err=%v", out, err)
	}
	result := h.run("", "mkdir", ".")
	if result.result.Status == 0 || !strings.Contains(result.stderr, "File exists") {
		t.Fatalf("mkdir without parents ignored existing root: %+v", result)
	}
}

func TestNativeFailureRetainsBoundedStderr(t *testing.T) {
	var stderr bytes.Buffer
	inv := invocation{stderr: &stderr, spec: toolcontract.CommandSpecification{Name: "mkdir"}}
	cause := errors.New("filesystem failure: " + strings.Repeat("x", 5000))
	result, err := inv.outcome(cause)
	if !errors.Is(err, cause) || result.Status != 1 || result.Terminal != toolcontract.ProducerFailed {
		t.Fatalf("failure outcome=%+v err=%v", result, err)
	}
	if !strings.HasPrefix(stderr.String(), "mkdir: filesystem failure: ") || stderr.Len() > 4110 {
		t.Fatalf("missing or unbounded diagnostic: %d bytes", stderr.Len())
	}
}
