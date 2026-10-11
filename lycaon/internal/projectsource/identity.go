// Package projectsource observes and changes files under attached project roots.
package projectsource

import (
	"context"
	"database/sql"
	"io"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type SourceMutationRecorder interface {
	Record(context.Context, sourceledger.RecordInput) error
	RecordBatch(context.Context, []sourceledger.RecordInput) error
	RecordBatchTx(context.Context, *sql.Tx, []sourceledger.RecordInput) error
}

type SourceHeadReader interface {
	ResolveHeadByFile(context.Context, string, sourcebranch.ID, string) (sourceledger.BranchHead, error)
}

type RecoveryStorage interface {
	BeginRecovery(context.Context, string, string) (*sourceledger.RecoveryWriter, error)
	CopyRecoveryFile(context.Context, string, io.Writer) error
	WalkRecovery(context.Context, string, string, int64, bool, func(sourceledger.RecoveryEntry) error) error
}

// ProjectSource supplies physical source identity without registry state.
type ProjectSource interface {
	SourceID() string
	SourceRoots() []projectroot.RootRef
	WorkspaceID() string
}

func rootByID(roots []projectroot.RootRef, id string) (projectroot.RootRef, bool) {
	for _, root := range roots {
		if root.ID == id {
			return root, true
		}
	}
	return projectroot.RootRef{}, false
}
