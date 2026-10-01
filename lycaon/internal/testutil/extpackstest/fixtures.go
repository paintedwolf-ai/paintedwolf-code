package extpackstest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// WriteMinimalPack writes a one-policy pack for the selected API epoch.
func WriteMinimalPack(t *testing.T, dir, id string, epoch int) {
	t.Helper()
	api := "^1.0.0"
	if epoch != 1 {
		api = "^99.0.0"
	}
	body := "manifest_version: 1\nid: " + id + "\nname: test\nversion: 1.0.0\ncompatibility:\n  extension_api: \"" + api + "\"\ndependencies:\n  painted-wolf/platform:\n    version: \"^1.0.0\"\n"
	MustWrite(t, filepath.Join(dir, "extension.yaml"), body)
	// Project-scope fixtures require an effective policy unit.
	MustWrite(t, filepath.Join(dir, "policy", "ACME_HELLO.yaml"),
		"id: ACME_HELLO\nemit: banner\nmessage: hi\neffect: warn\n")
}

// MustWrite writes one file, creating parent directories.
func MustWrite(t *testing.T, path, body string) {
	t.Helper()
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(path), 0o750))
	testutil.FailErr(t, "write file", os.WriteFile(path, []byte(body), 0o600))
}

// GitInitCommit initializes a repository in dir and commits its contents.
func GitInitCommit(t *testing.T, dir string) {
	t.Helper()
	gittest.InitCommit(t, dir, "init")
}

// GitCommitAll stages and commits everything in dir.
func GitCommitAll(t *testing.T, dir, message string) {
	t.Helper()
	gittest.CommitAll(t, dir, message)
}

// GitTag creates a tag in dir.
func GitTag(t *testing.T, dir, tag string) {
	t.Helper()
	gittest.Run(t, dir, "tag", tag)
}
