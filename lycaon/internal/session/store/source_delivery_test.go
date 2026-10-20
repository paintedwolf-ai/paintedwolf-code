package store

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDeliveredSourcePersistsExactWindowAndDoesNotQualifyUnseenBytes(t *testing.T) {
	dir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dir, "delivery.db"))
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	owner := NewSQL(database)
	owner.SetDataDir(dir)
	session, err := owner.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create source delivery session", err)
	captured := []byte("first unseen\nsecond shown\nthird unseen\n")
	page := []byte("second shown\n")
	sum := sha256.Sum256(captured)
	pin := toolcontract.SourcePin{SourceID: "source:fixture", Revision: "revision:one", Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(sum[:]), EndByte: int64(len(captured))}
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceStartByte: 13, SourceEndByte: 26, OutputEndByte: 13}
	observation, err := evidence.ConfirmDeliveredSource("src/fixture.txt", pin, captured, page, mapping, "response:one", "delivery:one")
	testutil.FailErr(t, "confirm actual page bytes", err)
	before, err := owner.LoadLedger(t.Context(), session.ID)
	testutil.FailErr(t, "read ledger before delivery commit", err)
	if len(before.Handles) != 0 {
		t.Fatal("capture alone populated observation ledger")
	}
	handle, err := owner.CommitDeliveredSource(t.Context(), session.ID, observation)
	testutil.FailErr(t, "commit exact delivered page", err)
	repeated, err := owner.CommitDeliveredSource(t.Context(), session.ID, observation)
	testutil.FailErr(t, "repeat acknowledged delivery", err)
	if repeated != handle {
		t.Fatal("delivery retry minted duplicate observation")
	}
	// A fresh recorder reads persisted metadata and captured page bodies; unseen
	// ranges remain unavailable after the original delivering component disappears.
	restored := NewSQL(database)
	restored.SetDataDir(dir)
	ledger, err := restored.LoadLedger(t.Context(), session.ID)
	testutil.FailErr(t, "restore delivered source ledger", err)
	record, ok := evidence.ResolveHandle(ledger, handle)
	if !ok || record.SourceDelivery == nil || record.SourceDelivery.Source != pin {
		t.Fatalf("delivery metadata missing: %+v", record)
	}
	for _, line := range []int{1, 3} {
		if yes, _ := evidence.VerifyFileRegion(record, evidence.Claim{Path: "src/fixture.txt", Line: line}); yes {
			t.Fatalf("unseen source line%d qualified", line)
		}
	}
	if yes, _ := evidence.VerifyFileRegion(record, evidence.Claim{Path: "src/fixture.txt", Line: 2, Excerpt: "second shown"}); !yes {
		t.Fatal("actual delivered exact window did not qualify")
	}
}
