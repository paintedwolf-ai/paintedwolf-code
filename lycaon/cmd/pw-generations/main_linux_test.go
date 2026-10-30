//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestOnlyTheFixedOperationsAreAccepted(t *testing.T) {
	for _, args := range [][]string{nil, {"install"}, {"install", "--family", "dmg"}, {"install", "--family=deb"},
		{"install", "--family", "deb", "extra"}, {"remove", "--all"}, {"purge"}, {"select", "/tmp"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("run(%q) = %v, want the usage refusal", args, err)
		}
	}
}
