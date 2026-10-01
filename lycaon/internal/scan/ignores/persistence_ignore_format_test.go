package ignores

import (
	"errors"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIgnorePublicationRefusesAWithdrawnMarker(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "publish marker", settingsoverlay.EnsureCurrentFormat(root))
	_, err := AddIgnoreEntry(root, IgnoreEntry{Path: "test/**", Reason: "fixtures"})
	testutil.FailErr(t, "write ignore", err)
	testutil.FailErr(t, "withdraw marker", os.Remove(settingsoverlay.FormatPath(root)))
	if _, err := AddIgnoreEntry(root, IgnoreEntry{Path: "other/**", Reason: "fixtures"}); !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("published after marker withdrawal: %v", err)
	}
}
