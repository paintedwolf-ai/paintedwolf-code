package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/localdata"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func TestRestoreTransactionRejectsSymlinkDirectory(t *testing.T) {
	config := t.TempDir()
	candidate := filepath.Join(config, localdata.RestoreStagingDirPrefix+"-11111111-1111-1111-1111-111111111111")
	testutil.FailErr(t, "link transaction directory", os.Symlink(t.TempDir(), candidate))
	if _, _, err := validateRestoreTransactionDir(config, candidate, localdata.RestoreStagingDirPrefix); err == nil {
		t.Fatal("symlinked transaction directory accepted")
	}
}
