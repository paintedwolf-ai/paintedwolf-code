package userpath

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConfiguredPathValidatesEveryEntry(t *testing.T) {
	root := t.TempDir()
	for _, raw := range []string{"", "relative", root + string(filepath.ListSeparator), root + "\n", root + "\x00"} {
		if _, err := Configured(raw, 8); err == nil {
			t.Errorf("accepted invalid PATH %q", raw)
		}
	}
	if _, err := Configured(joinList([]string{root, filepath.Join(root, "bin")}), 1); err == nil {
		t.Error("accepted excess entries")
	}
	snapshot, err := Configured(joinList([]string{root, root, filepath.Join(root, "bin")}), 8)
	testutil.FailErr(t, "resolve configured path", err)
	if snapshot.Source() != SourceConfigured || snapshot.Failure() != FailureNone || snapshot.Reason() != "" || len(snapshot.Entries()) != 2 {
		t.Errorf("configured snapshot = %+v", snapshot)
	}
}
