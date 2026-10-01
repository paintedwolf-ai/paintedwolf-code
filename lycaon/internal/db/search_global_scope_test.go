package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSearchGlobalScopeSpansProjectsScopedNarrows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "db.Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	projectA := testdbseed.DefaultProjectID
	projectB := "00000000-0000-4000-8000-0000000000b2"

	seedEvidenceMessage(t, sqlDB, projectA, "sess-a", "msg-a", "needle-token alpha")
	seedEvidenceMessage(t, sqlDB, projectB, "sess-b", "msg-b", "needle-token bravo")

	svc := search.NewService(sqlDB, decide.Reranker{}, nil)
	compileCtx := search.CompileContext{
		OriginProjectID:      projectA,
		ResolveProjectBySlug: func(string) (string, error) { return "", nil },
		RootsForProject:      func(string) ([]search.CodeRoot, error) { return nil, nil },
		AttachedProjectIDs:   func() ([]string, error) { return []string{projectA, projectB}, nil },
	}

	global, err := svc.Search(ctx, "needle-token", compileCtx)
	testutil.FailErr(t, "global search", err)
	globalProjects := hitProjectSet(global.Hits)
	if !globalProjects[projectA] || !globalProjects[projectB] {
		t.Fatalf("global search must span both projects; got projects %v (hits=%d)", globalProjects, len(global.Hits))
	}

	scoped, err := svc.Search(ctx, "needle-token project:current", compileCtx)
	testutil.FailErr(t, "scoped search", err)
	scopedProjects := hitProjectSet(scoped.Hits)
	if len(scoped.Hits) == 0 {
		t.Fatal("scoped search returned no hits")
	}
	if scopedProjects[projectB] {
		t.Fatalf("scoped search leaked project B: %v", scopedProjects)
	}
	if !scopedProjects[projectA] {
		t.Fatalf("scoped search missing origin project: %v", scopedProjects)
	}
	if len(global.Hits) < len(scoped.Hits) {
		t.Fatalf("global hits (%d) must be a superset of scoped hits (%d)", len(global.Hits), len(scoped.Hits))
	}
}

func seedEvidenceMessage(t *testing.T, sqlDB db.Handle, projectID, sessionID, messageID, content string) {
	t.Helper()
	ctx := context.Background()
	if projectID != testdbseed.DefaultProjectID {
		testdbseed.InsertProject(t, sqlDB, projectID)
	}
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	msg := api.Message{
		ID:        messageID,
		Role:      api.MessageRoleUser,
		Content:   content,
		CreatedAt: time.Now().UTC(),
		Grounding: &api.CitationGrounding{
			Traced: true,
			CitedEvidence: []api.CitationGroundingCitedEvidence{{
				Handle:  "read#1",
				Path:    "src/file.go",
				Line:    1,
				Excerpt: content,
				Verdict: api.CitationVerdictMatched,
			}},
		},
	}
	testdbseed.InsertSessionEntry(t, tx, "entry-"+msg.ID, sessionID, "utterance", msg.ID, 1)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts)
		VALUES (?, ?, ?, ?, ?, 'user', 'user', 'trusted', '', ?)
	`, msg.ID, "entry-"+msg.ID, sessionID, msg.Role, msg.Content, db.FormatTime(msg.CreatedAt))
	testutil.FailErr(t, "insert message", err)
	testutil.FailErr(t, "SyncMessageWriteThrough",
		search.SyncMessageWriteThrough(ctx, tx, projectID, sessionID, msg))
	testutil.FailErr(t, "Commit", tx.Commit())
}

func hitProjectSet(hits []search.Hit) map[string]bool {
	out := map[string]bool{}
	for _, h := range hits {
		out[h.ProjectID] = true
	}
	return out
}
