package browser

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
)

func TestMeasureProbeRefusalsRetainObservedSelectorAndCount(t *testing.T) {
	for _, tc := range []struct {
		code string
		node probeNode
	}{
		{"MEASURE_SELECTOR_EMPTY", probeNode{Count: 0}},
		{"MEASURE_SELECTOR_AMBIGUOUS", probeNode{Count: 3}},
		{"MEASURE_SELECTOR_INVALID", probeNode{Error: "invalid selector"}},
	} {
		t.Run(tc.code, func(t *testing.T) {
			result, err := measuredProbeNodes([]string{"#literal-🚀"}, []probeNode{tc.node})
			var reject *browserengine.RejectError
			if !errors.As(err, &reject) || reject.Code != tc.code || len(result) != 0 {
				t.Fatalf("probe fabricated geometry or lost refusal: %+v %v", result, err)
			}
			if reject.Data["measure_selector"] != "#literal-🚀" {
				t.Fatalf("selector lost: %+v", reject.Data)
			}
			if tc.node.Error == "" && reject.Data["count"] != tc.node.Count {
				t.Fatalf("match count lost: %+v", reject.Data)
			}
		})
	}
}
