package mutation

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func TestRedirectExactStreamValidatesLengthAndPinnedBase(t *testing.T) {
	for _, size := range []string{"small", "large"} {
		payload := "suffix\n"
		if size == "large" {
			payload = strings.Repeat(payload, 800000)
		}
		for _, action := range []string{"replace", "append", "under", "over", "changed during review"} {
			t.Run(size+"/"+action, func(t *testing.T) {
				h := newHarness(t, map[string]string{"out": "original\n"})
				target, err := h.owners.captureDestination(t.Context(), h.tc, h.invocation().Identity.ExecutionID, h.path("out"))
				testutil.FailErr(t, "pin destination", err)
				input := native.WriteInput{Path: target.abs, Exact: true, Source: strings.NewReader(payload), SourceBytes: int64(len(payload)),
					Admission: native.WriteExecutionPinned, Destination: target.pin, Append: action == "append"}
				if action == "under" {
					input.SourceBytes--
				}
				if action == "over" {
					input.SourceBytes++
				}
				if action == "changed during review" {
					h.tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
						return os.WriteFile(h.path("out"), []byte("human\n"), 0o644)
					}
				}
				result, err := h.owners.Operations.Write.Apply(t.Context(), input, h.tc)
				if action == "replace" || action == "append" {
					testutil.FailErr(t, "publish exact stream", err)
					want := payload
					if input.Append {
						want = "original\n" + payload
					}
					if h.read("out") != want || !result.Saved || result.PublishedSHA256 == "" || result.ResultBytes != len(want) {
						t.Fatalf("stream settlement %+v", result)
					}
				} else {
					if err == nil {
						t.Fatal("invalid stream or changed pin published")
					}
					want := "original\n"
					if action == "changed during review" {
						want = "human\n"
					}
					if h.read("out") != want {
						t.Fatal("refused stream replaced destination")
					}
				}
			})
		}
	}

}

func TestExactWriteRejectsAmbiguousOrUnboundedStreamBeforeEffects(t *testing.T) {
	h := newHarness(t, map[string]string{"out": "original\n"})
	for _, input := range []native.WriteInput{
		{Exact: true, Source: strings.NewReader("x"), Bytes: []byte{}},
		{Exact: true, Source: strings.NewReader("x"), SourceBytes: -1},
		{Exact: true, Source: strings.NewReader("x"), SourceBytes: math.MaxInt64},
		{Exact: true, SourceBytes: 1},
		{Source: strings.NewReader("x"), SourceBytes: 1},
	} {
		input.Path, input.Admission = h.path("out"), native.WriteExecutionPinned
		if _, err := h.owners.Operations.Write.Apply(t.Context(), input, h.tc); err == nil {
			t.Fatal("invalid exact input admitted")
		}
		if h.read("out") != "original\n" {
			t.Fatal("invalid input changed destination")
		}
	}
}
