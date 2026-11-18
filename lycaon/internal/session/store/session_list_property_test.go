package store

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionSummaryPagingMatchesFixtureModel(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertProject(t, sqlDB, "other-project")

	fixtures := seedSummaryStores(t, sqlDB)
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
							assertSummaryPagesMatch(t, sqlStore, fixtures, q)
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

func seedSummaryStores(t *testing.T, sqlDB db.Handle) []api.Session {
	var fixtures []api.Session
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
		seedSummarySession(t, sqlDB, sess)
		fixtures = append(fixtures, sess)
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
		seedSummarySession(t, sqlDB, sess)
		fixtures = append(fixtures, sess)
	}
	return fixtures
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

func seedSummarySession(t *testing.T, sqlDB db.Handle, sess api.Session) {
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
		nullableSummaryTime(sess.ArchivedAt), sess.PinRank, timelayout.Format(sess.CreatedAt),
		timelayout.Format(sess.ActivityAt), timelayout.Format(sess.UpdatedAt))
	testutil.FailErr(t, "insert session summary fixture", err)
}

func nullableSummaryTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timelayout.Format(*value)
}

func assertSummaryPagesMatch(t *testing.T, sqlStore *Repositories, fixtures []api.Session, q SummaryQuery) {
	t.Helper()
	var expected []api.Session
	for _, row := range fixtures {
		if row.ProjectID != q.ProjectID || row.ParentSessionID != "" || (row.ArchivedAt != nil) != q.Archived {
			continue
		}
		if q.Pinned != nil && (row.PinRank != nil) != *q.Pinned {
			continue
		}
		if !strings.Contains(strings.ToLower(row.Title), strings.ToLower(q.TitleQuery)) {
			continue
		}
		expected = append(expected, row)
	}
	value := func(row api.Session) string {
		switch q.Sort {
		case api.SessionListSortCreated:
			return timelayout.Format(row.CreatedAt)
		case api.SessionListSortTitle:
			return strings.ToLower(row.Title)
		case api.SessionListSortPin:
			return fmt.Sprintf("%020d", *row.PinRank)
		default:
			return timelayout.Format(row.ActivityAt)
		}
	}
	sort.Slice(expected, func(i, j int) bool {
		left, right := value(expected[i]), value(expected[j])
		if left == right {
			left, right = expected[i].ID, expected[j].ID
		}
		if q.Order == api.SessionListOrderAsc {
			return left < right
		}
		return left > right
	})
	expectedIDs := make([]string, 0, len(expected))
	for _, row := range expected {
		expectedIDs = append(expectedIDs, row.ID)
	}
	actualIDs := make([]string, 0, len(expected))
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, err := sqlStore.Sessions.ListProjectSummaries(t.Context(), q)
		testutil.FailErr(t, "list SQL page", err)
		if page.Total != len(expectedIDs) {
			t.Fatalf("total = %d, want %d", page.Total, len(expectedIDs))
		}
		actualIDs = append(actualIDs, summaryIDs(page.Sessions)...)
		if page.Next == nil {
			if !reflect.DeepEqual(actualIDs, expectedIDs) {
				t.Fatalf("session IDs = %v, want %v", actualIDs, expectedIDs)
			}
			return
		}
		q.After = page.Next
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
