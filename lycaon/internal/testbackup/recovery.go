// Package testbackup creates production-format retained-history recovery fixtures.
package testbackup

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

func Recovery(t *testing.T, database db.DBTX, path string) string {
	t.Helper()
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan recovery fixture", err)
	appVersion, _, err := db.ReadAppVersion(t.Context(), database)
	testutil.FailErr(t, "read recovery fixture version", err)
	root := filepath.Dir(path)
	err = backup.CaptureUpgradeRecovery(t.Context(), backup.CreateOpts{
		ConfigDir: root, DBPath: path, SQLDB: database, AppVersion: appVersion,
		SchemaUserVersion: db.SchemaVersion,
	}, plan, version.Version)
	testutil.FailErr(t, "capture full recovery fixture", err)
	archive, _, err := backup.LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "find recovery fixture", err)
	return archive
}
