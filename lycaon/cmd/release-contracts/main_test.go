package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

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
		{[]string{"--check-all", "--published-versions", "[]"}, 1},
		{[]string{"--check-all"}, 1},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), t.TempDir(), tc.args, &stdout, &stderr); code != tc.code || stderr.Len() == 0 {
			t.Fatalf("%q exit=%d want %d: %s", tc.args, code, tc.code, &stderr)
		}
	}
}

func TestRunCheckAllVerifiesCompatibilityAndPublishedMigrations(t *testing.T) {
	moduleRoot := configlayout.FindModuleRoot()
	if moduleRoot == "" {
		t.Fatal("FindModuleRoot returned empty inside checkout")
	}
	repoRoot := filepath.Dir(moduleRoot)

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), repoRoot, []string{"--check-all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("check-all default versions failed (exit %d): %s", code, &stderr)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(t.Context(), repoRoot, []string{"--check-all", "--published-versions", `["v99.99.99"]`}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "release migrations:") {
		t.Fatalf("check-all with missing migration tag exit=%d stderr=%q, want exit 1 with release migrations error", code, stderr.String())
	}
}
