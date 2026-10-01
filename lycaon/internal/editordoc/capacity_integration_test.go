//go:build integration

package editordoc

import (
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"testing"
)

func TestDocumentAtFileLimitSurvivesReplacementCompactionAndReload(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"large.txt": strings.Repeat("a", project.SourceWriteMaxBytes)})
	document := f.open(t, "large.txt")
	for _, character := range []string{"b", "c", "d"} {
		current, err := f.service.ReplaceSnapshot(t.Context(), document.ID, document.ProjectID, SnapshotReplacement{
			DocumentCommand: DocumentCommand{ClientID: "window", ExpectedRevision: document.Revision, OperationID: uuid.NewString()},
			Content:         strings.Repeat(character, project.SourceWriteMaxBytes), EOL: "lf",
		})
		testutil.FailErr(t, "replace text at encoded file limit", err)
		document = current
	}
	f.service.replicas.mu.Lock()
	f.service.replicas.evict(t.Context(), document.ID)
	f.service.replicas.mu.Unlock()
	reloaded, err := f.service.Join(t.Context(), document.ID, document.ProjectID, ReplicaJoin{ClientID: "new-window", Incarnation: uuid.NewString(), Epoch: document.Epoch})
	testutil.FailErr(t, "reload compacted document at limit", err)
	if reloaded.Draft != document.Draft || reloaded.Epoch != document.Epoch {
		t.Fatal("compaction lost accepted text or offline identity")
	}
}
