package mutation

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestDecodedStreamStopsExactlyAtExpansionLimit(t *testing.T) {
	for _, source := range []string{"abc", "abcd"} {
		t.Run(source, func(t *testing.T) {
			r := &boundedReader{r: strings.NewReader(source), maximum: 3}
			out, err := io.ReadAll(r)
			if string(out) != "abc" {
				t.Fatalf("decoded prefix=%q", out)
			}
			if source == "abc" {
				if err != nil {
					t.Fatalf("at-limit stream refused: %v", err)
				}
				return
			}
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["maximum"] != int64(3) || reject.Data["observed"] != int64(4) {
				t.Fatalf("over-limit stream error=%v", err)
			}
		})
	}
}

func TestCompressedStreamLimitAndCancellation(t *testing.T) {
	input := &gzipInput{ctx: t.Context(), reader: strings.NewReader("abcd"), maximum: 3}
	got, err := io.ReadAll(input)
	reject := toolrejection.AsToolReject(err)
	if string(got) != "abc" || reject == nil || reject.Data["limit_id"] != "archive_input_bytes" || reject.Data["observed"] != int64(4) {
		t.Fatalf("compressed prefix=%q error=%v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	input = &gzipInput{ctx: ctx, reader: strings.NewReader("x"), maximum: 3}
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) || input.observed != 0 {
		t.Fatalf("canceled compressed read: %v bytes=%d", err, input.observed)
	}
}

func TestStreamingExpansionUsesObservedCompressedPrefix(t *testing.T) {
	for _, known := range []int64{-1, 3} {
		t.Run(map[bool]string{true: "stdin", false: "known file"}[known < 0], func(t *testing.T) {
			input := &gzipInput{ctx: t.Context(), observed: 3}
			output := &gzipOutput{input: input, reader: strings.NewReader("1234567"), maximum: 100, ratio: 2, knownBytes: known}
			got, err := io.ReadAll(output)
			reject := toolrejection.AsToolReject(err)
			if string(got) != "123456" || reject == nil || reject.Data["maximum"] != int64(6) || reject.Data["observed"] != int64(7) {
				t.Fatalf("expanded prefix=%q error=%v", got, err)
			}
		})
	}
}
