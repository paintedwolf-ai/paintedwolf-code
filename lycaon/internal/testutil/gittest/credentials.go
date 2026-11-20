package gittest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// UserCredentials configures a real read-only helper in an isolated user config.
func UserCredentials(t testing.TB, username, password string) {
	t.Helper()
	dir := t.TempDir()
	helper := filepath.Join(dir, "credential-helper")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	body := "#!/bin/sh\nif [ \"$1\" = get ]; then\n  printf '%s\\n' " + quote("username="+username) + " " + quote("password="+password) + "\nfi\n"
	testutil.FailErr(t, "write credential helper", os.WriteFile(helper, []byte(body), 0o700))
	config := filepath.Join(dir, "gitconfig")
	testutil.FailErr(t, "write user Git config", os.WriteFile(config, []byte("[credential]\n\thelper = "+strconv.Quote("!"+quote(helper))+"\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", config)
}
