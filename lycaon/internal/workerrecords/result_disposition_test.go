package workerrecords_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/workerrecords"
)

func TestResultDispositionPrefersChangeStateThenCheckpointThenAccounting(t *testing.T) {
	for _, tc := range []struct {
		change, checkpoint, accounted, want string
	}{
		{workerrecords.ChangeLanded, "cp-1", "t", workerrecords.ChangeLanded},
		{"", "cp-1", "t", workerrecords.ResultOpen},
		{"", "", "t", workerrecords.ResultAccounted},
		{"", "", "", workerrecords.ResultUnaccounted},
	} {
		if got := workerrecords.ResultDisposition(tc.change, tc.checkpoint, tc.accounted); got != tc.want {
			t.Errorf("ResultDisposition(%q, %q, %q) = %q, want %q", tc.change, tc.checkpoint, tc.accounted, got, tc.want)
		}
	}
}
