package workeroutcomes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// MergeWorkerUntrustedIntoParent propagates a child's external-content evidence.
func MergeWorkerUntrustedIntoParent(ctx context.Context, sessionStore Sessions, transcriptStore Transcript, evidenceStore Evidence, parentID, childID string) error {
	if sessionStore == nil || transcriptStore == nil || evidenceStore == nil {
		return nil
	}
	parentID = strings.TrimSpace(parentID)
	childID = strings.TrimSpace(childID)
	if parentID == "" || childID == "" {
		return nil
	}

	childEv, err := evidenceStore.LoadLedger(ctx, childID)
	if err != nil {
		return fmt.Errorf("load child evidence ledger: %w", err)
	}
	if !ledgerMarksUntrustedContent(childEv) {
		return nil
	}
	if err := evidenceStore.SeedUntrustedContent(ctx, parentID); err != nil {
		return err
	}
	for _, raw := range evidence.ObservedURLsSorted(childEv) {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if _, err := url.Parse(u); err != nil {
			continue
		}
		rec := evidence.Record{
			Handle:   workerURLHandle(u),
			Kind:     "web",
			Shape:    evidence.ShapeURL,
			Fidelity: evidence.FidelityStructured,
			URL:      u,
		}
		if err := evidenceStore.UpsertEvidenceRecord(ctx, parentID, rec); err != nil {
			return err
		}
	}
	return nil
}

func ledgerMarksUntrustedContent(ledger evidence.Ledger) bool {
	for _, rec := range ledger.Handles {
		if evidence.RecordMarksUntrustedContent(rec) {
			return true
		}
	}
	return false
}

func workerURLHandle(rawURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawURL)))
	return evidence.WorkerURLHandlePrefix + hex.EncodeToString(sum[:8])
}
