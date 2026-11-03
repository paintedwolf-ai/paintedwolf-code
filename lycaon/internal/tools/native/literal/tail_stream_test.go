package literal

import (
	"bytes"
	"fmt"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"io"
	"strings"
	"testing"
)

func TestTailStreamsMatchSeekableWindowsAcrossChunkBoundaries(t *testing.T) {
	h := formHarness(t)
	content := strings.Repeat("a", 32767) + "\n" + strings.Repeat("b", 65537) + "\nc\nd"
	seed(t, h.root, map[string]string{"large": content})
	for _, option := range []struct {
		flag, count string
		window      window
	}{
		{"-n", "1", window{count: 1}}, {"-n", "2", window{count: 2}}, {"-n", "0", window{}},
		{"-n", "+2", window{count: 2, fromStart: true}},
		{"-c", "4", window{bytes: true, count: 4}}, {"-c", "+3", window{bytes: true, count: 3, fromStart: true}},
	} {
		want := content[tailStart([]byte(content), option.window):]
		for _, operand := range []string{"-", "large"} {
			input := ""
			if operand == "-" {
				input = content
			}
			h.exec(input, "tail", option.flag, option.count, operand).expect(t, fmt.Sprint(option, operand), want, "", 0)
		}
	}
}

type wrappedEOFTailInput struct{ *strings.Reader }

func (r wrappedEOFTailInput) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.Len() == 0 {
		return n, fmt.Errorf("source exhausted: %w", io.EOF)
	}
	return n, err
}
func TestTailStartBeyondWrappedEOFCompletesAndReleases(t *testing.T) {
	h := formHarness(t)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load program limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "admit program memory", err)
	h.owners.Revisions.Memory = memory
	var stdout, stderr bytes.Buffer
	input := wrappedEOFTailInput{Reader: strings.NewReader("abc")}
	result, err := h.owners.Invoke(t.Context(), h.tc, mustPaths(t, h), requestFor(h, "tail", "-c", "+8"), commandprogram.Streams{Input: io.NopCloser(input), Output: &stdout, Error: &stderr})
	testutil.FailErr(t, "read tail beyond exhausted source", err)
	if input.Len() != 0 || stdout.Len() != 0 || stderr.Len() != 0 || result.Status != 0 || !result.Complete || !result.PublicationEligible || result.EffectCertainty != toolcontract.NoEffect || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("remaining=%d stdout=%q stderr=%q result=%+v memory=%+v", input.Len(), stdout.String(), stderr.String(), result, memory.InUse())
	}
}

func TestDumpStartBeyondWrappedEOFCompletesAndReleases(t *testing.T) {
	h := formHarness(t)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load program limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "admit program memory", err)
	h.owners.Revisions.Memory = memory
	var stdout, stderr bytes.Buffer
	input := wrappedEOFTailInput{Reader: strings.NewReader("abc")}
	result, err := h.owners.Invoke(t.Context(), h.tc, mustPaths(t, h), requestFor(h, "xxd", "-s", "8"), commandprogram.Streams{Input: io.NopCloser(input), Output: &stdout, Error: &stderr})
	testutil.FailErr(t, "read dump beyond exhausted source", err)
	if input.Len() != 0 || stdout.Len() != 0 || stderr.Len() != 0 || result.Status != 0 || !result.Complete || !result.PublicationEligible || result.EffectCertainty != toolcontract.NoEffect || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("remaining=%d stdout=%q stderr=%q result=%+v memory=%+v", input.Len(), stdout.String(), stderr.String(), result, memory.InUse())
	}
}
