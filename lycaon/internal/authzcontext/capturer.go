package authzcontext

import (
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Capturer bundles the SQL-backed authz write path (context seal + decision ledger).
type Capturer struct {
	Store    *SQLStore
	Sealer   *Sealer
	Ledger   *Ledger
	Recorder LedgerRecorder
}

// NewSQLCapturer binds sealing and decision recording to one SQL store.
func NewSQLCapturer(database db.Handle, audit AuditConfig, profiles map[string]sandbox.ToolProfile) *Capturer {
	if database == nil {
		return nil
	}
	store := NewSQLStore(database)
	ledger := &Ledger{Store: store, Audit: audit}
	return &Capturer{
		Store:    store,
		Sealer:   &Sealer{Store: store, Profiles: profiles},
		Ledger:   ledger,
		Recorder: LedgerRecorder{Ledger: ledger},
	}
}
