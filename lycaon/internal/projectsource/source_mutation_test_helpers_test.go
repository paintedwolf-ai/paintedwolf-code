package projectsource

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"testing"

	"github.com/google/uuid"
)

func writeProjectSource(p *Project, req SourceWriteRequest) (*SourceWriteResult, error) {
	return NewSourceMutationService(nil, nil).Write(context.Background(), uuid.NewString(), p, req)
}

func createProjectSourceEntry(p *Project, req SourceEntryCreateRequest) (string, error) {
	return NewSourceMutationService(nil, nil).Create(context.Background(), uuid.NewString(), p, req)
}

func renameProjectSource(p *Project, req SourceRenameRequest) (*SourceLifecycleResult, error) {
	return NewSourceMutationService(nil, nil).Rename(context.Background(), uuid.NewString(), p, req)
}

func copyProjectSource(p *Project, req SourceCopyRequest) (*SourceLifecycleResult, error) {
	return NewSourceMutationService(nil, nil).Copy(context.Background(), uuid.NewString(), p, req)
}

func deleteProjectSource(p *Project, req SourceDeleteRequest) error {
	return NewSourceMutationService(nil, nil).Delete(context.Background(), uuid.NewString(), p, req)
}

func testSourceOperationMemory(t testing.TB) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := newSourceOperationMemory()
	testutil.FailErr(t, "create source operation memory", err)
	return memory
}
