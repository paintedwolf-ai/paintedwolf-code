package secretcap

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestPutReplaysAnOperationOnlyWithItsOwnInput: a retried operation id
// answers the capability it created, and any changed input is refused.
func TestPutReplaysAnOperationOnlyWithItsOwnInput(t *testing.T) {
	service, _, _ := testService(t)
	first := PutRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "composer-1", Name: "deploy token", Purpose: "deploys", Scope: ScopeChat,
		Origin: OriginComposerMarked, PersonID: testOwner(t, service), Value: "replayed-secret-value-0001",
	}
	created, err := service.Put(t.Context(), first)
	testutil.FailErr(t, "create", err)
	replayed, err := service.Put(t.Context(), first)
	testutil.FailErr(t, "replay", err)
	if replayed.Created || replayed.Metadata.Reference != created.Metadata.Reference {
		t.Fatalf("replay = %+v, want the capability %s it created", replayed, created.Metadata.Reference)
	}
	for name, change := range map[string]func(*PutRequest){
		"name":    func(r *PutRequest) { r.Name = "other token" },
		"purpose": func(r *PutRequest) { r.Purpose = "other" },
		"value":   func(r *PutRequest) { r.Value = "replayed-secret-value-0002" },
		"chat":    func(r *PutRequest) { r.ChatSessionID = "root-2" },
	} {
		changed := first
		change(&changed)
		if _, err := service.Put(t.Context(), changed); !errors.Is(err, ErrOperationReused) {
			t.Errorf("replay with a different %s: err = %v, want ErrOperationReused", name, err)
		}
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list", err)
	if len(listed) != 1 {
		t.Fatalf("stored %d capabilities, want the one the operation created", len(listed))
	}
}

// TestGenerateReplaysWithoutComparingTheMintedValue: each call mints a new
// value, so a replay of the same request answers the first capability.
func TestGenerateReplaysWithoutComparingTheMintedValue(t *testing.T) {
	service, _, _ := testService(t)
	req := GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "generate-1", Name: "session key", Purpose: "signing", Scope: ScopeChat, Format: FormatHex, Bytes: 32,
	}
	first, err := service.Generate(t.Context(), req)
	testutil.FailErr(t, "generate", err)
	again, err := service.Generate(t.Context(), req)
	testutil.FailErr(t, "replay", err)
	if again.Reference != first.Reference {
		t.Fatalf("replayed generate = %s, want %s", again.Reference, first.Reference)
	}
}
