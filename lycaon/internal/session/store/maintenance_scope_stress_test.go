//go:build stress

package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStressMaintenanceScopesAcrossYearsOfSessionHistory(t *testing.T) {
	for _, count := range []int{5000, 50000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			root := t.TempDir()
			rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, root)
			_, err := database.ExecContext(t.Context(), `WITH RECURSIVE numbers(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<?)
			INSERT INTO sessions(id,project_id,owner_person_id,workspace_root_id,posture,status,created_at,activity_at,updated_at)
			SELECT printf('session-%06d',n),?,(SELECT id FROM people WHERE role = 'owner'),?,'build','idle','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z' FROM numbers`, count, testdbseed.DefaultProjectID, rootID)
			testutil.FailErr(t, "seed historical sessions", err)
			_, err = database.ExecContext(t.Context(), `INSERT INTO evidence_records(session_id,project_id,handle,ordinal,kind,marks_untrusted,content_blob_sha256)
			SELECT id,project_id,'web#1',1,'web',1,? FROM sessions`, strings.Repeat("a", 64))
			testutil.FailErr(t, "seed intentionally unreadable evidence bodies", err)
			s := NewSQL(database)
			var after string
			seen, pages := 0, 0
			pageStarted := time.Now()
			for {
				ids, err := s.WorkspaceNotificationIDs(t.Context(), root, after)
				testutil.FailErr(t, "page metadata without reading evidence", err)
				if len(ids) == 0 {
					break
				}
				seen += len(ids)
				pages++
				after = ids[len(ids)-1]
			}
			if seen != count || pages != (count+maintenanceSessionBatch-1)/maintenanceSessionBatch {
				t.Fatalf("unexpected scope work: seen=%d pages=%d", seen, pages)
			}
			pageElapsed := time.Since(pageStarted)
			trustStarted := time.Now()
			untrusted, err := s.SessionUntrustedContentResult(t.Context(), "session-000001")
			testutil.FailErr(t, "read indexed trust metadata without body", err)
			if !untrusted {
				t.Fatal("lost durable untrusted-content metadata")
			}
			t.Logf("sessions=%d pages=%d metadata_paging=%s indexed_trust_lookup=%s", count, pages, pageElapsed, time.Since(trustStarted))
		})
	}
}
