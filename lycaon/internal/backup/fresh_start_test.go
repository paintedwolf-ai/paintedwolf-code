package backup_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFreshStartPreservesARecoveryCopyAndDeviceConfiguration(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store, err := db.Open(dbPath)
	testutil.FailErr(t, "open live store", err)
	_, err = store.ExecContext(ctx,
		`INSERT INTO projects(id, last_opened_at, created_at) VALUES('project-1', 't', 't')`)
	testutil.FailErr(t, "seed live project", err)

	onboarding := localdata.FirstRunOnboardingRelPath()
	seedFiles := map[string]string{
		onboarding:                         `{"key":"onboarding","value":{"firstRunSetupCompleted":true}}`,
		"projects/project-1/history.txt":   "project history",
		"source-observations.db":           "projection",
		"providers.local.yaml":             "providers: []\n",
		"credential-vault.age":             "encrypted vault bytes",
		"credential-vault-identity.age":    "wrapped identity bytes",
		"approvals.yaml":                   "rules: []\n",
		"app-state-v1/646973706c6179.json": `{"key":"display","value":{"diffWordWrap":true}}`,
	}
	for rel, body := range seedFiles {
		path := filepath.Join(configDir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte(body), 0o600))
	}

	result, err := backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir:  configDir,
		DBPath:     dbPath,
		SQLDB:      store,
		AppVersion: "test",
	})
	testutil.FailErr(t, "stage fresh start", err)
	if !result.RestartRequired {
		t.Fatal("fresh start did not require restart")
	}
	assertProjectCount(t, ctx, store, 1)
	for _, rel := range []string{"store.db", onboarding, "projects/project-1/history.txt", "source-observations.db"} {
		if _, err := os.Stat(filepath.Join(result.RecoveryCopyPath, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("recovery copy missing %s: %v", rel, err)
		}
	}
	testutil.FailErr(t, "close live store", store.Close())
	testutil.FailErr(t, "apply fresh start", backup.ApplyPending(configDir))

	fresh := testdbfixture.OpenPath(t, dbPath)
	assertProjectCount(t, ctx, fresh, 0)
	for _, rel := range []string{onboarding, "projects", "source-observations.db"} {
		if _, err := os.Stat(filepath.Join(configDir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("fresh start retained %s: %v", rel, err)
		}
	}
	for _, rel := range []string{
		"providers.local.yaml",
		"credential-vault.age",
		"credential-vault-identity.age",
		"approvals.yaml",
		"app-state-v1/646973706c6179.json",
	} {
		if _, err := os.Stat(filepath.Join(configDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("fresh start removed device state %s: %v", rel, err)
		}
	}
}

func TestFreshStartCanReplaceAnIncompatibleStore(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	refused := []byte("database bytes from another version")
	testutil.FailErr(t, "write refused store", os.WriteFile(dbPath, refused, 0o600))

	result, err := backup.StageFreshStart(ctx, backup.FreshStartOpts{
		ConfigDir:  configDir,
		DBPath:     dbPath,
		AppVersion: "test",
	})
	testutil.FailErr(t, "stage fresh start", err)
	recovered, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, "store.db"))
	testutil.FailErr(t, "read refused recovery copy", err)
	if string(recovered) != string(refused) {
		t.Fatalf("recovery copy=%q want %q", recovered, refused)
	}
	testutil.FailErr(t, "apply fresh start", backup.ApplyPending(configDir))

	fresh := testdbfixture.OpenPath(t, dbPath)
	assertProjectCount(t, ctx, fresh, 0)
}

func assertProjectCount(t *testing.T, ctx context.Context, store interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, want int) {
	t.Helper()
	var got int
	testutil.FailErr(t, "count projects", store.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&got))
	if got != want {
		t.Fatalf("project count=%d want %d", got, want)
	}
}
