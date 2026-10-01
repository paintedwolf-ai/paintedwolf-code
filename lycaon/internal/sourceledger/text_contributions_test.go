package sourceledger

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestContributionSelectionUsesIdentityKindAndRevision(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "a.txt", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, OperationID: "create", After: []byte("base")})
	head, err := store.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "a.txt")
	testutil.FailErr(t, "resolve file", err)
	person, err := store.people.HostOwner(ctx)
	testutil.FailErr(t, "read contributing person", err)
	tx, err := store.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin contributions", err)
	defer func() { _ = tx.Rollback() }()
	for i := uint32(1); i <= 1000; i++ {
		c := TextContribution{ProjectID: "p1", FileID: head.FileID, DocumentID: "document", Epoch: 1, Revision: int64(i), OperationID: fmt.Sprint(i), Origin: api.SourceChangeOriginUser, PersonID: person.ID, CreatedAt: time.Now().UTC(),
			Inserted: []TextIdentityRange{{Client: 7, Start: i, End: i + 1}}, Deleted: []TextIdentityRange{{Client: 8, Start: 42, End: 43}}}
		testutil.FailErr(t, "record contribution", RecordTextContributionTx(ctx, tx, c))
	}
	testutil.FailErr(t, "commit contributions", tx.Commit())
	selected, err := store.DocumentContributions(ctx, "document", 1, ContributionSelection{ThroughRevision: 500, Inserted: []TextSpan{{Client: 7, Clock: 42, Length: 1}, {Client: 7, Clock: 900, Length: 1}, {Client: 8, Clock: 42, Length: 1}}})
	testutil.FailErr(t, "select visible identity", err)
	if len(selected) != 1 || selected[0].OperationID != "42" {
		t.Fatalf("selected unrelated history: %+v", selected)
	}
}
