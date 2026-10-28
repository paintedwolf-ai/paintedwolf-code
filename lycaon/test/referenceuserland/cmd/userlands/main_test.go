package main

import "testing"

func TestRunReportsUsageWithoutACommand(t *testing.T) {
	if got := run(nil); got != 2 {
		t.Fatalf("run without a command = %d, want the usage status 2", got)
	}
}
