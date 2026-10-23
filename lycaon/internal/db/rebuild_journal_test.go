package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func preparedRebuildFixture(t *testing.T) (string, rebuildJournal) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "custom.db")
	work, err := os.MkdirTemp(root, StoreUpgradeDirectoryPrefix)
	testutil.FailErr(t, "create work directory", err)
	identity, err := fspath.EntryIdentity(work)
	testutil.FailErr(t, "identify work directory", err)
	record := rebuildJournal{Version: 1, Database: "custom.db", Directory: filepath.Base(work), DirectoryIdentity: identity, State: "prepared"}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		testutil.FailErr(t, "seed original file", os.WriteFile(path+suffix, []byte("original"+suffix), 0o600))
		identity, err := fspath.EntryIdentity(path + suffix)
		testutil.FailErr(t, "identify original", err)
		record.Original = append(record.Original, rebuildFile{Suffix: suffix, Identity: identity})
	}
	replacement := filepath.Join(work, "replacement.db")
	testutil.FailErr(t, "seed replacement", os.WriteFile(replacement, []byte("verified replacement"), 0o600))
	record.ReplacementIdentity, err = fspath.EntryIdentity(replacement)
	testutil.FailErr(t, "identify replacement", err)
	testutil.FailErr(t, "record publication decision", writeRebuildJournal(path, record))
	return path, record
}

func TestRebuildPublicationResumesEveryDurablePrefix(t *testing.T) {
	for prefix := range 6 {
		t.Run(fmt.Sprint(prefix), func(t *testing.T) {
			path, record := preparedRebuildFixture(t)
			interrupted := errors.New("process interrupted after durable move")
			moves := 0
			if prefix > 0 {
				err := finishRebuildPublication(path, record, func(string) error {
					moves++
					if moves == prefix {
						return interrupted
					}
					return nil
				})
				if !errors.Is(err, interrupted) {
					t.Fatalf("interruption = %v", err)
				}
			}
			testutil.FailErr(t, "resume interrupted publication", ResumeStoreUpgrade(path))
			testutil.FailErr(t, "repeat completed publication", ResumeStoreUpgrade(path))
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "read installed database", err)
			if string(raw) != "verified replacement" {
				t.Fatal("replacement not published")
			}
			for _, file := range record.Original {
				old := filepath.Join(filepath.Dir(path), record.Directory, "original.db"+file.Suffix)
				raw, err := os.ReadFile(old)
				testutil.FailErr(t, "read preserved original", err)
				if string(raw) != "original"+file.Suffix {
					t.Fatal("original database/journal pair changed")
				}
				if file.Suffix != "" {
					if _, err := os.Stat(path + file.Suffix); !os.IsNotExist(err) {
						t.Fatalf("old sidecar accompanied replacement: %v", err)
					}
				}
			}
			testutil.FailErr(t, "retire original after readiness", CompleteStoreUpgrade(path))
			testutil.FailErr(t, "repeat retirement", CompleteStoreUpgrade(path))
			if _, err := os.Stat(rebuildMarker(path)); !os.IsNotExist(err) {
				t.Fatalf("publication journal remains: %v", err)
			}
		})
	}
}

func TestRebuildPublicationRefusesChangedDestination(t *testing.T) {
	path, record := preparedRebuildFixture(t)
	old := filepath.Join(filepath.Dir(path), record.Directory, "original.db")
	testutil.FailErr(t, "occupy original destination", os.WriteFile(old, []byte("unrelated file"), 0o600))
	if err := ResumeStoreUpgrade(path); err == nil {
		t.Fatal("publication overwrote a different destination")
	}
	for _, check := range []struct{ path, want string }{{path, "original"}, {old, "unrelated file"}} {
		raw, err := os.ReadFile(check.path)
		testutil.FailErr(t, "read refused file", err)
		if string(raw) != check.want {
			t.Fatal("identity refusal changed a file")
		}
	}
}

func TestRebuildInterruptedBuildLeavesOriginalUntouched(t *testing.T) {
	path, record := preparedRebuildFixture(t)
	record.State = "building"
	testutil.FailErr(t, "record interrupted build", writeRebuildJournal(path, record))
	if err := CompleteStoreUpgrade(path); err == nil {
		t.Fatal("unbuilt replacement reported complete")
	}
	testutil.FailErr(t, "discard unpublished build", ResumeStoreUpgrade(path))
	for _, file := range record.Original {
		raw, err := os.ReadFile(path + file.Suffix)
		testutil.FailErr(t, "read untouched original", err)
		if string(raw) != "original"+file.Suffix {
			t.Fatal("interrupted build changed original")
		}
	}
}

func TestRebuildPublicationRejectsTrailingJournalContent(t *testing.T) {
	path, record := preparedRebuildFixture(t)
	file, err := os.OpenFile(rebuildMarker(path), os.O_APPEND|os.O_WRONLY, 0)
	testutil.FailErr(t, "open publication journal", err)
	_, err = file.WriteString(` {"state":"building"}`)
	testutil.FailErr(t, "append conflicting record", err)
	testutil.FailErr(t, "close journal", file.Close())
	if err := ResumeStoreUpgrade(path); err == nil {
		t.Fatal("accepted ambiguous publication decision")
	}
	for _, original := range record.Original {
		testutil.FailErr(t, "preserve original identity", requireRebuildIdentity(path+original.Suffix, original.Identity))
	}
}
