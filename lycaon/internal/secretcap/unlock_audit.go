package secretcap

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/presence"
)

// RecordUnlock audits an unlock presence opened, inside the transaction of the
// approval that opened it.
func RecordUnlock(ctx context.Context, tx *sql.Tx, projectID string, unlock presence.Unlock) error {
	if err := db.New(tx).CreateVaultUnlock(ctx, db.CreateVaultUnlockParams{
		ID: unlock.ID, ProjectID: projectID, ChatSessionID: unlock.ChatSessionID, PersonID: unlock.PersonID,
		Authenticator: unlock.Authenticator, WindowLabel: unlock.WindowLabel,
		UnlockedAt: db.FormatTime(unlock.UnlockedAt),
	}); err != nil {
		return fmt.Errorf("record vault unlock: %w", err)
	}
	return nil
}

// RecordUnlockEnd audits why an unlock ended. A failed write leaves the row
// open, and the next boot closes it as restart.
func (s *Service) RecordUnlockEnd(ctx context.Context, unlock presence.Unlock, reason presence.EndReason, at time.Time) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.queries.EndVaultUnlock(ctx, db.EndVaultUnlockParams{
		EndedAt: nullable(db.FormatTime(at)), EndReason: nullable(string(reason)), ID: unlock.ID,
	}); err != nil {
		slog.WarnContext(ctx, "vault unlock end write failed", "unlock_id", unlock.ID, "reason", reason)
	}
}

// CloseUnlocksLeftOpen ends the unlocks a stopped engine left open. Unlocks
// live only in memory, so at boot none is open; run it before any can be.
func (s *Service) CloseUnlocksLeftOpen(ctx context.Context) error {
	if _, err := s.queries.EndOpenVaultUnlocks(ctx, nullable(db.FormatTime(s.now()))); err != nil {
		return fmt.Errorf("close vault unlocks left open: %w", err)
	}
	return nil
}
