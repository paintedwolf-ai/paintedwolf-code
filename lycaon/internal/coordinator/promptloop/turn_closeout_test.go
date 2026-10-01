package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestIterationRunwayFires(t *testing.T) {
	coordinator := &api.Session{ID: "coordinator"}
	worker := &api.Session{ID: "worker", ParentSessionID: "coordinator"}
	cases := []struct {
		name      string
		sess      *api.Session
		iterIndex int
		maxIter   int
		want      bool
	}{
		{"coordinator budget at threshold", coordinator, 290, 300, true},
		{"coordinator budget before threshold", coordinator, 289, 300, false},
		{"coordinator small budget never fires", coordinator, 9, 19, false},
		{"coordinator budget exactly twice threshold", coordinator, 10, 20, true},
		{"worker runway is a third of its ceiling", worker, 13, 20, true},
		{"worker before its runway", worker, 12, 20, false},
		{"worker runway stops at ten rounds", worker, 30, 40, true},
		{"worker runway is at least three rounds", worker, 1, 4, true},
		{"worker with one round has no runway", worker, 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := iterationRunwayFires(tc.sess, tc.iterIndex, tc.maxIter); got != tc.want {
				t.Fatalf("iterationRunwayFires(%s, %d, %d) = %v want %v", tc.sess.ID, tc.iterIndex, tc.maxIter, got, tc.want)
			}
		})
	}
}
