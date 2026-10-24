package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/testutil"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"syscall"
	"testing"
)

func TestRebuildFailurePreservesOperationalCause(t *testing.T) {
	for _, cause := range []error{syscall.ENOSPC, syscall.EIO, syscall.EACCES} {
		err := classifyUpgradeFailure(cause, 4)
		if !errors.Is(err, cause) || errors.Is(err, ErrStoreIncompatible) {
			t.Fatalf("operational error was replaced by schema/corruption claim: %v", err)
		}
	}
}

func injectRebuildFault(t *testing.T, source, phase string, cause error) {
	t.Helper()
	var active atomic.Bool
	active.Store(true)
	t.Cleanup(func() { active.Store(false) })
	root := filepath.Dir(source)
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		if !active.Load() {
			return nil
		}
		u, err := url.Parse(dsn)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, u.Path)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.Base(u.Path) != "replacement.db" {
			return nil
		}
		if phase == "copy" {
			_, err = conn.ExecContext(context.Background(), `PRAGMA max_page_count=8`, nil)
			return err
		}
		if u.RawQuery == "_pragma=synchronous(full)" {
			return cause
		}
		return nil
	})
}

func TestRebuildCopyAndSealFailuresPreserveOriginal(t *testing.T) {
	for _, tc := range []struct {
		phase string
		cause error
	}{{"copy", nil}, {"seal", syscall.ENOSPC}, {"seal", syscall.EACCES}, {"seal", syscall.EIO}} {
		t.Run(tc.phase+fmt.Sprint(tc.cause), func(t *testing.T) {
			source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
			testutil.FailErr(t, "seal byte-comparison baseline", sealRebuiltStore(t.Context(), source))
			before, err := os.ReadFile(source)
			testutil.FailErr(t, "read original", err)
			injectRebuildFault(t, source, tc.phase, tc.cause)
			err = UpgradeStaged(t.Context(), source)
			if tc.phase == "copy" {
				var full *sqlite.Error
				if !errors.As(err, &full) || full.Code()&0xff != sqlite3.SQLITE_FULL {
					t.Fatalf("copy did not reach SQLITE_FULL: %v", err)
				}
			} else if !errors.Is(err, tc.cause) {
				t.Fatalf("seal lost cause: %v", err)
			}
			if errors.Is(err, ErrStoreIncompatible) {
				t.Fatalf("operational failure diagnosed damage: %v", err)
			}
			after, err := os.ReadFile(source)
			testutil.FailErr(t, "read refused original", err)
			if !bytes.Equal(before, after) {
				t.Fatal("failed preparation changed original")
			}
			if _, err := os.Stat(rebuildMarker(source)); !os.IsNotExist(err) {
				t.Fatalf("failed build left journal: %v", err)
			}
			work, err := filepath.Glob(filepath.Join(filepath.Dir(source), StoreUpgradeDirectoryPrefix+"*"))
			testutil.FailErr(t, "inspect failed work", err)
			if len(work) != 0 {
				t.Fatal("failed build left working copies")
			}
		})
	}
}
