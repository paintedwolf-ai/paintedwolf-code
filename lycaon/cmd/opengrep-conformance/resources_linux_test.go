package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxProcessMemoryUsesFieldsAfterCompleteCommand(t *testing.T) {
	fields := strings.Fields("S 10 42 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 123 0")
	group, rss, err := linuxProcessMemory("41 (worker name ) with spaces) " + strings.Join(fields, " "))
	testutil.FailErr(t, "parse process memory", err)
	if group != 42 || rss != 123 {
		t.Fatalf("process fields shifted: group=%d rss=%d", group, rss)
	}
	for _, raw := range []string{"", "41 (worker) S 10 42", strings.Replace(strings.Join(fields, " "), "123", "-1", 1)} {
		if _, _, err := linuxProcessMemory(raw); err == nil {
			t.Fatalf("invalid process stat accepted: %q", raw)
		}
	}
}
