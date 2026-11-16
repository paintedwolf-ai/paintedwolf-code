package main

import (
	"bytes"
	"testing"
)

func TestRunRefusesMalformedInvocations(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{[]string{"--check-all", "--check"}, 2},
		{[]string{"--check"}, 2},
		{[]string{"painted-wolf/bugbash", "bugbash"}, 2},
		{[]string{"--undefined"}, 2},
		{[]string{"--check-all", "--published-versions", "null"}, 1},
		{[]string{"--check-all", "--published-versions", "{}"}, 1},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), t.TempDir(), tc.args, &stdout, &stderr); code != tc.code || stderr.Len() == 0 {
			t.Fatalf("%q exit=%d want %d: %s", tc.args, code, tc.code, &stderr)
		}
	}
}
