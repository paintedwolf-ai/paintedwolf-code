package guidance

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// CloseoutEvidence is the merged coordinator + worker leg ledger for closeout grounding.
type CloseoutEvidence struct {
	evidence.Ledger
}

// EvidenceLeg is one worker leg whose ledger a closeout may cite. LegID
// namespaces the leg's handles; without one the child session does.
type EvidenceLeg struct {
	ChildSessionID string
	LegID          string
}

// Namespace is the prefix the leg's handles carry in a merged ledger.
func (l EvidenceLeg) Namespace() string {
	if id := strings.TrimSpace(l.LegID); id != "" {
		return id
	}
	return strings.TrimSpace(l.ChildSessionID)
}

// CloseoutEvidenceReader reads what a closeout may cite: the session's own
// ledger and the ledgers of the worker legs it dispatched. The legs come from
// durable dispatch records, so a compacted transcript never narrows them.
type CloseoutEvidenceReader interface {
	EvidenceLedgerReader
	// WorkerLegs lists the legs a session dispatched at or after since; a zero
	// since lists every leg the session dispatched.
	WorkerLegs(ctx context.Context, parentSessionID string, since time.Time) ([]EvidenceLeg, error)
}

// UnionCloseoutEvidence merges the coordinator ledger with the ledgers of the
// worker legs dispatched since the current user intent.
func UnionCloseoutEvidence(ctx context.Context, reader CloseoutEvidenceReader, parentSessionID string, history []api.Message) (CloseoutEvidence, error) {
	out := CloseoutEvidence{Ledger: evidence.InitLedger()}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if reader == nil || parentSessionID == "" {
		return out, nil
	}
	legs, err := reader.WorkerLegs(ctx, parentSessionID, userIntentTime(history))
	if err != nil {
		return CloseoutEvidence{}, err
	}
	out, err = UnionLegEvidence(ctx, reader, legs)
	if err != nil {
		return CloseoutEvidence{}, err
	}
	coordEv, err := reader.LoadLedger(ctx, parentSessionID)
	if err != nil {
		return CloseoutEvidence{}, err
	}
	if len(coordEv.Handles) > 0 {
		evidence.MergeLedger(&out.Ledger, coordEv)
	}
	return out, nil
}

// userIntentTime is when the current user intent arrived, or zero when the
// history holds none and every leg of the session counts.
func userIntentTime(history []api.Message) time.Time {
	if i := api.UserIntentBoundary(history); i > 0 {
		return history[i-1].CreatedAt
	}
	return time.Time{}
}

// UnionLegEvidence merges the ledgers of worker legs, each under its namespace.
func UnionLegEvidence(ctx context.Context, reader EvidenceLedgerReader, legs []EvidenceLeg) (CloseoutEvidence, error) {
	out := CloseoutEvidence{Ledger: evidence.InitLedger()}
	if reader == nil {
		return out, nil
	}
	seen := map[string]struct{}{}
	for _, leg := range legs {
		childID := strings.TrimSpace(leg.ChildSessionID)
		if childID == "" {
			continue
		}
		if _, dup := seen[childID]; dup {
			continue
		}
		seen[childID] = struct{}{}
		childEv, err := reader.LoadLedger(ctx, childID)
		if err != nil {
			return out, err
		}
		if len(childEv.Handles) == 0 {
			continue
		}
		evidence.MergeLedger(&out.Ledger, evidence.NamespaceLedger(childEv, leg.Namespace()))
	}
	return out, nil
}
