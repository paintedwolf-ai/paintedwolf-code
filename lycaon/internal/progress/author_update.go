package progress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// AuthorUpdate validates a complete replacement before publication.
type AuthorUpdate struct {
	content             string
	expectedRevision    string
	reconciliationToken string
}

// AuthorUpdateRejection preserves the progress catalog's code and typed facts.
type AuthorUpdateRejection struct {
	Code string
	Data map[string]any
}

func (e *AuthorUpdateRejection) Error() string { return e.Code }

// PrepareAuthorUpdate validates checklist structure without writing.
func PrepareAuthorUpdate(content string) (AuthorUpdate, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return AuthorUpdate{}, fmt.Errorf("content is required")
	}
	if utf8.RuneCountInString(content) > ContentMaxChars {
		return AuthorUpdate{}, &AuthorUpdateRejection{Code: "PROGRESS_DOCUMENT_TOO_LARGE", Data: map[string]any{"max_characters": ContentMaxChars}}
	}
	if code, data, ok := validateAuthorProgressStructure(content); !ok {
		return AuthorUpdate{}, &AuthorUpdateRejection{Code: code, Data: data}
	}
	return AuthorUpdate{content: content}, nil
}

// AuthorUpdateResult records checklist publication and finish acknowledgement.
type AuthorUpdateResult struct {
	ReconciliationStatus string   `json:"reconciliation_status"`
	Updated              bool     `json:"document_changed"`
	StateChanged         bool     `json:"state_changed"`
	DeliverableClosed    bool     `json:"deliverable_closed"`
	Snapshot             Snapshot `json:"snapshot"`
}

// AdvancesWorkflow distinguishes deliverable advancement from document bookkeeping.
func (r AuthorUpdateResult) AdvancesWorkflow() bool {
	return r.StateChanged || r.ReconciliationStatus == "acknowledged"
}

// ExpectRevision binds a prepared replacement to an observed document.
func (u AuthorUpdate) ExpectRevision(revision string) AuthorUpdate {
	u.expectedRevision = strings.TrimSpace(revision)
	return u
}

// Apply checks the observed revision and publishes one atomic replacement.
func (u AuthorUpdate) Apply(ctx context.Context, store Store, root string, reconciler Reconciler) (AuthorUpdateResult, error) {
	if err := ctx.Err(); err != nil {
		return AuthorUpdateResult{}, err
	}
	if strings.TrimSpace(root) == "" {
		return AuthorUpdateResult{}, fmt.Errorf("session required")
	}
	if u.content == "" || store == nil {
		return AuthorUpdateResult{}, fmt.Errorf("prepared progress and store required")
	}
	result := AuthorUpdateResult{ReconciliationStatus: "not_requested"}
	if err := u.validateReconciliation(ctx, store, root, reconciler); err != nil {
		return result, err
	}
	var previous string
	snapshot, err := store.Update(ctx, root, func(current Snapshot) (string, error) {
		previous = current.Content
		if current.Content != "" && u.expectedRevision == "" {
			return "", &AuthorUpdateRejection{Code: "PROGRESS_REVISION_REQUIRED", Data: map[string]any{"snapshot": current}}
		}
		if u.expectedRevision != "" && u.expectedRevision != current.Revision {
			return "", &AuthorUpdateRejection{Code: "PROGRESS_REVISION_CHANGED", Data: map[string]any{"snapshot": current}}
		}
		if code, data, ok := ValidateAuthorProgress(current.Content, u.content); !ok {
			return "", &AuthorUpdateRejection{Code: code, Data: data}
		}
		return u.content, nil
	})
	if err != nil {
		var rejection *AuthorUpdateRejection
		if errors.As(err, &rejection) {
			result.Snapshot = snapshot
			if reconciler != nil {
				result.Snapshot.Reconciliation = reconciler.Reconciliation(root)
			}
			if rejection.Code == "PROGRESS_REVISION_CHANGED" || rejection.Code == "PROGRESS_REVISION_REQUIRED" {
				rejection.Data["snapshot"] = result.Snapshot
			}
		}
		return result, err
	}
	result.Snapshot = snapshot
	result.Updated = previous != snapshot.Content
	transitions := ClassifyTransitions(previous, snapshot.Content)
	result.StateChanged = transitions.Changed
	result.DeliverableClosed = transitions.Closed
	if result.Updated {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: root, Prev: previous})
	}
	if u.reconciliationToken != "" {
		result.ReconciliationStatus = "changed"
		if reconciler.Acknowledge(root, u.reconciliationToken) {
			result.ReconciliationStatus = "acknowledged"
		}
	}
	if reconciler != nil {
		result.Snapshot.Reconciliation = reconciler.Reconciliation(root)
	}
	return result, nil
}
