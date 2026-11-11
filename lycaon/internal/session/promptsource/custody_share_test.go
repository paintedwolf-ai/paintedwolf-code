package promptsource

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

type custodyRepo struct {
	ToolsRepository
	sessions  map[string]*api.Session
	committed map[string][]evidence.ProgramHTTPProvenance
}

func (r *custodyRepo) Get(_ context.Context, id string) (*api.Session, error) {
	return r.sessions[id], nil
}

func (r *custodyRepo) CommitProgramHTTPProvenance(_ context.Context, sessionID, _ string, prov evidence.ProgramHTTPProvenance) (string, error) {
	r.committed[sessionID] = append(r.committed[sessionID], prov)
	return "http_response#1", nil
}

// A worker's response saved to a workspace file is custody the chat holds
// too, so the chat reading that file later knows its origin. The chat's own
// results and results without external custody share nothing.
func TestWorkerCustodyIsSharedWithTheChat(t *testing.T) {
	repo := &custodyRepo{
		sessions: map[string]*api.Session{
			"chat":   {ID: "chat"},
			"worker": {ID: "worker", ParentSessionID: "chat"},
		},
		committed: map[string][]evidence.ProgramHTTPProvenance{},
	}
	m := &Tools{Sessions: repo}
	args := map[string]any{"url": "https://api.example.test/data", "response_path": "out/data.json"}
	result := `{"status":200,"response_path":"out/data.json","written":true}`

	if err := m.shareCustodyWithChat(t.Context(), "worker", "/work", "http_request", args, result); err != nil {
		t.Fatal(err)
	}
	got := repo.committed["chat"]
	if len(got) != 1 || got[0].UntrustedContent || !slices.Contains(got[0].CustodyPaths, "out/data.json") || !slices.Contains(got[0].ObservedURLs, "https://api.example.test/data") {
		t.Fatalf("chat custody = %+v", got)
	}

	if err := m.shareCustodyWithChat(t.Context(), "chat", "/work", "http_request", args, result); err != nil {
		t.Fatal(err)
	}
	if err := m.shareCustodyWithChat(t.Context(), "worker", "/work", "read", map[string]any{"path": "a.go"}, "package a"); err != nil {
		t.Fatal(err)
	}
	if len(repo.committed["chat"]) != 1 {
		t.Fatalf("shared custody that was not a worker's external write: %+v", repo.committed["chat"])
	}
}
