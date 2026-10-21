//go:build darwin || linux

package projectsource

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPlatformPublicationPreservesHeldTargetAndAuthoritativeJournal(t *testing.T) {
	service, journal, path := preparedAgentWrite(t)
	old, err := os.Open(path)
	testutil.FailErr(t, "hold pre-publication file", err)
	defer old.Close()
	result, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path), Source: strings.NewReader("after"), Mode: 0600,
		BeforeCommit: func(fseffect.Target, fseffect.Result) error {
			return os.Rename(path, path+".moved")
		},
	})
	testutil.FailErr(t, "publish through held parent", err)
	if result.Publication != fseffect.PublicationVerified {
		t.Fatalf("actual publication = %v", result.Publication)
	}
	before, err := io.ReadAll(old)
	testutil.FailErr(t, "read held original", err)
	if string(before) != "before" {
		t.Fatalf("held target changed: %q", before)
	}
	// A caller failure after publication cannot erase the domain's actual effect.
	completionErr := errors.New("fixture completion failure")
	err = journal.Finish(t.Context(), completionErr)
	var applied *sourceeffect.AppliedError
	if !errors.As(err, &applied) || applied.OperationID != journal.ID() {
		t.Fatalf("publication completion = %v", err)
	}
	row, found, err := service.Journal.load(t.Context(), journal.ID())
	testutil.FailErr(t, "read authoritative journal", err)
	if !found || row.Status != sourceMutationCommitted {
		t.Fatalf("journal lost completed publication: found=%v row=%+v", found, row)
	}
	var operations int
	testutil.FailErr(t, "count actual source operations", service.Journal.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_operations WHERE operation_key=?`, journal.ID()).Scan(&operations))
	if operations != 1 {
		t.Fatalf("source operations = %d", operations)
	}
}
