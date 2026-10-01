package findings

import (
	"context"
	"testing"
)

func appendMemoryFinding(t *testing.T, store *MemoryStore, sessionID, agent, summary, ref string) bool {
	t.Helper()
	appended, err := store.Append(context.Background(), sessionID, agent, summary, ref, "")
	if err != nil {
		t.Fatalf("append finding: %v", err)
	}
	return appended
}

func TestMemoryStore_AppendIdempotent(t *testing.T) {
	store := NewMemoryStore()
	const session = "root-1"
	if !appendMemoryFinding(t, store, session, "job-a", "same note", "pkg/foo.go") {
		t.Fatal("first append should insert")
	}
	if appendMemoryFinding(t, store, session, "job-a", "same note", "pkg/foo.go") {
		t.Fatal("duplicate append should be idempotent")
	}
	if appendMemoryFinding(t, store, session, "job-a", "  same note  ", "pkg/foo.go") {
		t.Fatal("whitespace-normalized duplicate should be idempotent")
	}
	if !appendMemoryFinding(t, store, session, "job-a", "other note", "pkg/foo.go") {
		t.Fatal("different summary should insert")
	}
	if !appendMemoryFinding(t, store, session, "job-b", "same note", "pkg/foo.go") {
		t.Fatal("different agent should insert")
	}
	got, err := store.List(context.Background(), session, 10)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("list = %d want 3 distinct findings", len(got))
	}
}
