package mutation

import (
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
