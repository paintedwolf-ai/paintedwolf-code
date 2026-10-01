package store

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionSummaryPagingMatchesMemoryModel(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertProject(t, sqlDB, "other-project")

	memory := NewMemory()
	seedSummaryStores(t, sqlDB, memory)
	sqlStore := NewSQL(sqlDB)
	sorts := []api.SessionListSort{
		api.SessionListSortActivity,
		api.SessionListSortCreated,
		api.SessionListSortTitle,
		api.SessionListSortPin,
	}
	orders := []api.SessionListOrder{api.SessionListOrderAsc, api.SessionListOrderDesc}
	queries := []string{"", "alpha", "%", "_", `\`}
	yes, no := true, false
	pinnedFilters := []*bool{nil, &yes, &no}

	for _, archived := range []bool{false, true} {
		for _, pinned := range pinnedFilters {
			for _, sortBy := range sorts {
				if sortBy == api.SessionListSortPin && (pinned == nil || !*pinned) {
					continue
				}
				for _, order := range orders {
					for _, titleQuery := range queries {
						q := SummaryQuery{
							ProjectID:  testdbseed.DefaultProjectID,
							Archived:   archived,
							Pinned:     pinned,
							TitleQuery: titleQuery,
							Sort:       sortBy,
							Order:      order,
							Limit:      3,
						}
						name := fmt.Sprintf("archived=%t/pinned=%s/sort=%s/order=%s/query=%q",
							archived, pinnedFilterName(pinned), sortBy, order, titleQuery)
						t.Run(name, func(t *testing.T) {
							assertSummaryPagesMatch(t, sqlStore, memory, q)
						})
					}
				}
			}
		}
	}
}

func pinnedFilterName(pinned *bool) string {
	if pinned == nil {
		return "any"
	}
	return fmt.Sprintf("%t", *pinned)
}

func seedSummaryStores(t *testing.T, sqlDB db.Handle, memory *Memory) {
	t.Helper()
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 30; i++ {
		projectID := testdbseed.DefaultProjectID
		if i%9 == 0 {
			projectID = "other-project"
		}
		sess := api.Session{
			ID:         fmt.Sprintf("session-%02d", i),
			ProjectID:  projectID,
			Title:      summaryFixtureTitle(i),
			Posture:    api.SessionPostureBuild,
			Status:     api.SessionStatusIdle,
			CreatedAt:  base.Add(time.Duration(i%4) * time.Minute),
			ActivityAt: base.Add(time.Duration(i%6) * time.Minute),
			UpdatedAt:  base.Add(time.Duration(i%7) * time.Minute),
		}
		if i%4 == 0 {
			archived := base.Add(time.Hour)
			sess.ArchivedAt = &archived
		} else if i%3 == 1 {
			// Ranks keep the gaps an unpin leaves.
			rank := 40 - i
			sess.PinRank = &rank
		}
		seedSummarySession(t, sqlDB, memory, sess)
	}
	for i := 0; i < 4; i++ {
		sess := api.Session{
			ID:              fmt.Sprintf("child-%02d", i),
			ProjectID:       testdbseed.DefaultProjectID,
			ParentSessionID: "session-01",
			Title:           "alpha child",
			Posture:         api.SessionPostureBuild,
			Status:          api.SessionStatusIdle,
			CreatedAt:       base,
			ActivityAt:      base,
			UpdatedAt:       base,
		}
		seedSummarySession(t, sqlDB, memory, sess)
	}
}

func summaryFixtureTitle(i int) string {
	switch i % 5 {
	case 0:
		return fmt.Sprintf("Alpha %02d", i)
	case 1:
		return fmt.Sprintf("literal_%02d", i)
	case 2:
		return fmt.Sprintf("percent%%%02d", i)
	case 3:
		return `path\name`
	default:
		return "same"
	}
}

func seedSummarySession(t *testing.T, sqlDB db.Handle, memory *Memory, sess api.Session) {
	t.Helper()
	var parent any
	if sess.ParentSessionID != "" {
		parent = sess.ParentSessionID
	}
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (
			id, project_id, owner_person_id, title, posture, status, parent_session_id,
			archived_at, pin_rank, created_at, activity_at, updated_at
		) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sess.ID, sess.ProjectID, sess.Title, sess.Posture, sess.Status, parent,
		nullableSummaryTime(sess.ArchivedAt), sess.PinRank, db.FormatTime(sess.CreatedAt),
		db.FormatTime(sess.ActivityAt), db.FormatTime(sess.UpdatedAt))
	testutil.FailErr(t, "insert session summary fixture", err)
	copy := sess
	memory.sessions[sess.ID] = &copy
}

func nullableSummaryTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return db.FormatTime(*value)
}

func assertSummaryPagesMatch(t *testing.T, sqlStore *SQL, memory *Memory, q SummaryQuery) {
	t.Helper()
	var sqlIDs []string
	seen := make(map[string]struct{})
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		sqlPage, err := sqlStore.ListProjectSummaries(t.Context(), q)
		testutil.FailErr(t, "list SQL page", err)
		memoryPage, err := memory.ListProjectSummaries(t.Context(), q)
		testutil.FailErr(t, "list memory page", err)

		if sqlPage.Total != memoryPage.Total {
			t.Fatalf("page %d totals: SQL=%d memory=%d", pageNumber, sqlPage.Total, memoryPage.Total)
		}
		if !reflect.DeepEqual(summaryIDs(sqlPage.Sessions), summaryIDs(memoryPage.Sessions)) {
			t.Fatalf("page %d ids: SQL=%v memory=%v", pageNumber,
				summaryIDs(sqlPage.Sessions), summaryIDs(memoryPage.Sessions))
		}
		if !reflect.DeepEqual(sqlPage.Next, memoryPage.Next) {
			t.Fatalf("page %d cursors: SQL=%+v memory=%+v", pageNumber, sqlPage.Next, memoryPage.Next)
		}
		for _, id := range summaryIDs(sqlPage.Sessions) {
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("session %q appeared on multiple pages", id)
			}
			seen[id] = struct{}{}
			sqlIDs = append(sqlIDs, id)
		}
		if sqlPage.Next == nil {
			if len(sqlIDs) != sqlPage.Total {
				t.Fatalf("walked %d rows, total is %d", len(sqlIDs), sqlPage.Total)
			}
			return
		}
		q.After = sqlPage.Next
	}
	t.Fatal("pagination did not terminate")
}

func summaryIDs(rows []api.SessionSummary) []string {
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}
