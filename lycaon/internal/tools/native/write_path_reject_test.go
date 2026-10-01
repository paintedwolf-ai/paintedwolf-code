package native

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestWritePathRejectRequiresTypedRefusal(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		preserve bool
	}{
		{
			name: "copied markers",
			err:  fmt.Errorf("%s UNKNOWN_ROOT_LABEL\n%s UNKNOWN_ROOT_LABEL", hostmarker.Rejected, hostmarker.CodeLine),
		},
		{
			name:     "typed observation",
			err:      &tools.ToolReject{Code: "UNKNOWN_ROOT_LABEL"},
			preserve: true,
		},
		{
			name:     "typed refusal without markers",
			err:      guidance.NewRefusal("UNKNOWN_ROOT_LABEL", "fixture refusal"),
			preserve: true,
		},
		{
			name:     "wrapped refusal",
			err:      fmt.Errorf("resolve path: %w", guidance.NewRefusal("UNKNOWN_ROOT_LABEL", "fixture refusal")),
			preserve: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			denied := errors.New("fixture write denial")
			calls := 0
			got := writePathReject(t.Context(), nil, "file.txt", "implement", "write", tc.err, func(path string) error {
				calls++
				if path != "file.txt" {
					t.Fatalf("denied path = %q, want file.txt", path)
				}
				return denied
			})
			want, wantCalls := denied, 1
			if tc.preserve {
				want, wantCalls = tc.err, 0
			}
			if !errors.Is(got, want) || calls != wantCalls {
				t.Fatalf("write rejection = %v (%d denials), want %v (%d denials)", got, calls, want, wantCalls)
			}
		})
	}
}
