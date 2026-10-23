package persistence

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

type integrityStartupSink struct{ phases []startupprotocol.Phase }

func (s *integrityStartupSink) Phase(phase startupprotocol.Phase) error {
	s.phases = append(s.phases, phase)
	return nil
}
func (*integrityStartupSink) Ready(int) error     { return nil }
func (*integrityStartupSink) Failed(string) error { return nil }

func TestStoreRecoveryChecksReachStartupProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, marker string
		want         []startupprotocol.Phase
	}{
		{name: "clean", marker: "clean_shutdown"},
		{name: "unclean", marker: "clean_shutdown", want: []startupprotocol.Phase{startupprotocol.PhaseRecoveryStructure}},
		{name: "audit", marker: "integrity_audit_failed", want: []startupprotocol.Phase{startupprotocol.PhaseRecoveryIndexes, startupprotocol.PhaseRecoveryReferences}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			database, err := db.Open(path)
			testutil.FailErr(t, "create fixture", err)
			testutil.FailErr(t, "stamp app version", db.New(database).UpsertStoreMeta(t.Context(), db.UpsertStoreMetaParams{Key: "app_version", Value: version.Version}))
			testutil.FailErr(t, "close fixture", database.Close())
			if tc.name != "clean" {
				writer, err := sql.Open("sqlite", path)
				testutil.FailErr(t, "open fixture writer", err)
				_, err = writer.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, tc.marker, "0")
				testutil.FailErr(t, "set recovery marker", err)
				testutil.FailErr(t, "close fixture writer", writer.Close())
			}
			sink := &integrityStartupSink{}
			builder := &Runtime{logger: slog.Default(), startup: sink}
			database, err = builder.openUpgradeableStore(t.Context(), path)
			testutil.FailErr(t, "open recovering store", err)
			t.Cleanup(func() { _ = database.Close() })
			if !reflect.DeepEqual(sink.phases, tc.want) {
				t.Fatalf("startup phases = %v, want %v", sink.phases, tc.want)
			}
		})
	}
}
