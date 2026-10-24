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
	record := rebuildJournal{Version: 2, Database: "custom.db", Directory: filepath.Base(work), DirectoryIdentity: identity, State: "prepared"}
	testutil.FailErr(t, "seed original file", os.WriteFile(path, []byte("original"), 0o600))
	record.OriginalIdentity, err = fspath.EntryIdentity(path)
	testutil.FailErr(t, "identify original", err)
	replacement := filepath.Join(work, "replacement.db")
	testutil.FailErr(t, "seed replacement", os.WriteFile(replacement, []byte("verified replacement"), 0o600))
	record.ReplacementIdentity, err = fspath.EntryIdentity(replacement)
	testutil.FailErr(t, "identify replacement", err)
	testutil.FailErr(t, "record publication decision", writeRebuildJournal(path, record))
	return path, record
}

func TestRebuildPublicationResumesEveryDurablePrefix(t *testing.T) {
	for prefix := 1; prefix <= 2; prefix++ {
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
			old := filepath.Join(filepath.Dir(path), record.Directory, "original.db")
			raw, err = os.ReadFile(old)
			testutil.FailErr(t, "read preserved original", err)
			if string(raw) != "original" {
				t.Fatal("original changed")
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
	if err := finishRebuildPublication(path, record, nil); err == nil {
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
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read untouched original", err)
	if string(raw) != "original" {
		t.Fatal("interrupted build changed original")
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
	testutil.FailErr(t, "preserve original identity", requireRebuildIdentity(path, record.OriginalIdentity))
}
