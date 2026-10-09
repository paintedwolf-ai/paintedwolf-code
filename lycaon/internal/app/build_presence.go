package app

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
)

// unlockRecorder audits an unlock in the commit of the approval that
// opened it.
type unlockRecorder struct{}

func (unlockRecorder) RecordUnlockTx(ctx context.Context, tx *sql.Tx, projectID string, unlock presence.Unlock) error {
	return secretcap.RecordUnlock(ctx, tx, projectID, unlock)
}
