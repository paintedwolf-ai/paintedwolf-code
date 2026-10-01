package historyretention

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDatabaseUsageFollowsConfiguredStoreAndItsSidecars(t *testing.T) {
	for _, name := range []string{"store.db", "isolated.db", "history café #1.sqlite"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			storePath := filepath.Join(root, name)
			database := testdbfixture.OpenPath(t, storePath)
			if name != "store.db" {
				testutil.FailErr(t, "seed unrelated default-name file", os.WriteFile(filepath.Join(root, "store.db"), []byte("unrelated"), 0o600))
			}
			s := New(database, storePath, nil)
			testutil.FailErr(t, "measure configured database", s.refreshUsage(t.Context()))
			status, err := s.Status(t.Context())
			testutil.FailErr(t, "read measured usage", err)
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				info, statErr := os.Stat(storePath + suffix)
				found := false
				for _, lane := range status.Lanes {
					if lane.ID != "store.db"+suffix {
						continue
					}
					found = true
					testutil.FailErr(t, "stat measured database file", statErr)
					if lane.StoredBytes != info.Size() {
						t.Fatalf("%s measured %d bytes; configured file has %d", lane.ID, lane.StoredBytes, info.Size())
					}
				}
				if found != (statErr == nil) {
					t.Fatalf("database suffix %q: measured=%v stat=%v", suffix, found, statErr)
				}
			}
		})
	}
}

func TestStorageUsageIncludesNativeEditorOutbox(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")
	database := testdbfixture.OpenPath(t, storePath)
	directory := filepath.Join(root, editoroutbox.Directory(), "document")
	testutil.FailErr(t, "create native outbox directory", os.MkdirAll(directory, 0o700))
	testutil.FailErr(t, "seed native outbox bytes", os.WriteFile(filepath.Join(directory, "records-1.log"), []byte("retained editor bytes"), 0o600))
	service := New(database, storePath, nil)
	testutil.FailErr(t, "measure native editor storage", service.refreshUsage(t.Context()))
	status, err := service.Status(t.Context())
	testutil.FailErr(t, "read usage report", err)
	for _, lane := range status.Lanes {
		if lane.ID == editoroutbox.Directory() {
			if lane.StoredBytes != int64(len("retained editor bytes")) {
				t.Fatalf("outbox size = %d", lane.StoredBytes)
			}
			return
		}
	}
	t.Fatal("native editor storage missing from usage report")
}
