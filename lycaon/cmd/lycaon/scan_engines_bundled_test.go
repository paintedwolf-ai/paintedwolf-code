package main

import "testing"

func TestVerifyBundledRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--root"}, {"--root", "/tmp", "extra"}, {"--unknown"}} {
		if err := runVerifyBundledEngine(args); err == nil {
			t.Fatalf("accepted invalid verification arguments: %v", args)
		}
	}
}
