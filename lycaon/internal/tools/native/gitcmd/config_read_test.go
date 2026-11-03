package gitcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Serial: sets process-wide GIT_CONFIG_GLOBAL via t.Setenv.
func TestConfigReadDoorKeepsLocalQueriesAndRefusesWrites(t *testing.T) {
	h := newEmptyHarness(t)
	configPath := filepath.Join(h.root, ".git", "config")
	before, err := os.ReadFile(configPath)
	testutil.FailErr(t, "read repository config", err)
	globalPath := filepath.Join(t.TempDir(), "global.config")
	global := []byte("[user]\nname = Global author\n")
	testutil.FailErr(t, "seed global config", os.WriteFile(globalPath, global, 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", globalPath)
	reader := git.CommandReader{}
	list := h.git("config", "--local", "--list")
	for _, tc := range []struct {
		args   []string
		want   string
		status int
	}{
		{[]string{"config", "--get", "user.name"}, "Ann Author\n", 0},
		{[]string{"config", "--get", "no.such"}, "", 1},
		{[]string{"config", "--get-regexp", "^user.name$"}, "user.name Ann Author\n", 0},
		{[]string{"config", "--list"}, list, 0},
		{[]string{"config", "-l"}, list, 0},
		{[]string{"config", "--get-regexp", "--unset-all=user.name"}, "", 1},
	} {
		t.Run(strings.Join(tc.args[1:], " "), func(t *testing.T) {
			var out bytes.Buffer
			_, status, err := reader.ReadCommand(t.Context(), h.root, tc.args, []string{"user.name=Injected author"}, &out)
			testutil.FailErr(t, "query local config", err)
			if status != tc.status || out.String() != tc.want {
				t.Fatalf("query = %d %q, want %d %q", status, out.String(), tc.status, tc.want)
			}
		})
	}
	for _, args := range [][]string{
		{"config", "user.name", "Changed"},
		{"config", "--unset", "user.name"},
		{"config", "--unset-all", "user.name"},
		{"config", "--add", "user.name", "Changed"},
		{"config", "--replace-all", "user.name", "Changed"},
		{"config", "--get", "user.name", "--list"},
		{"config", "--get", "user.name", "--get", "user.email"},
		{"config", "--local", "--list"},
		{"config", "--global", "--list"},
		{"config", "--system", "--list"},
		{"config", "--file", globalPath, "--list"},
		{"config", "--includes", "--list"},
	} {
		var out bytes.Buffer
		diagnostics, status, err := reader.ReadCommand(t.Context(), h.root, args, nil, &out)
		if err == nil || status != -1 || len(diagnostics) != 0 || out.Len() != 0 {
			t.Fatalf("invalid config form %v reached execution: status=%d output=%q diagnostics=%q err=%v", args, status, out.String(), diagnostics, err)
		}
	}
	after, err := os.ReadFile(configPath)
	testutil.FailErr(t, "reread repository config", err)
	if !bytes.Equal(after, before) {
		t.Fatal("config query or refused write changed repository configuration")
	}
	after, err = os.ReadFile(globalPath)
	testutil.FailErr(t, "reread global config", err)
	if !bytes.Equal(after, global) {
		t.Fatal("config query or refused write changed global configuration")
	}
}
