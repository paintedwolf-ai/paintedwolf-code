package progress

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// AuthorUpdate validates a complete replacement before publication.
type AuthorUpdate struct {
	content             string
	expectedRevision    string
	reconciliationToken string
	itemIDs             []string
	itemIDsProvided     bool
	itemIDsInvalid      bool
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

// ExpectItemIDs binds checklist rows to existing item IDs; empty strings mint new IDs.
func (u AuthorUpdate) ExpectItemIDs(itemIDs ...string) AuthorUpdate {
	u.itemIDsProvided = true
	u.itemIDsInvalid = false
	u.itemIDs = make([]string, len(itemIDs))
	for i, id := range itemIDs {
		trimmed := strings.TrimSpace(id)
		if id != "" && trimmed == "" {
			u.itemIDsInvalid = true
		}
		u.itemIDs[i] = trimmed
	}
	return u
}

// ExpectItemIDsFromArgs extracts optional item_ids from tool call arguments.
func (u AuthorUpdate) ExpectItemIDsFromArgs(args map[string]any) AuthorUpdate {
	if args == nil {
		return u
	}
	raw, present := args["item_ids"]
	if !present || raw == nil {
		return u
	}
	u.itemIDsProvided = true
	u.itemIDsInvalid = false
	switch v := raw.(type) {
	case []string:
		return u.ExpectItemIDs(v...)
	case []*string:
		u.itemIDs = make([]string, len(v))
		for i, ptr := range v {
			if ptr == nil {
				continue
			}
			trimmed := strings.TrimSpace(*ptr)
			if trimmed == "" {
				u.itemIDsInvalid = true
			}
			u.itemIDs[i] = trimmed
		}
	case []any:
		u.itemIDs = make([]string, len(v))
		for i, elem := range v {
			if elem == nil {
				continue
			}
			s, ok := elem.(string)
			if !ok {
				u.itemIDsInvalid = true
				continue
			}
			trimmed := strings.TrimSpace(s)
			if trimmed == "" {
				u.itemIDsInvalid = true
			}
			u.itemIDs[i] = trimmed
		}
	default:
		u.itemIDsInvalid = true
	}
	return u
}

// Apply checks the observed revision and item identities and publishes one atomic replacement.
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
	nextRowCount := len(ChecklistItems(u.content))
	var previous Snapshot
	snapshot, err := store.Update(ctx, root, func(current Snapshot) (Document, error) {
		previous = current
		if current.Content != "" && u.expectedRevision == "" {
			return Document{}, &AuthorUpdateRejection{Code: "PROGRESS_REVISION_REQUIRED", Data: map[string]any{"snapshot": current}}
		}
		if u.expectedRevision != "" && u.expectedRevision != current.Revision {
			return Document{}, &AuthorUpdateRejection{Code: "PROGRESS_REVISION_CHANGED", Data: map[string]any{"snapshot": current}}
		}
		if len(current.Items()) != 0 && !u.itemIDsProvided {
			return Document{}, &AuthorUpdateRejection{Code: "PROGRESS_REVISION_REQUIRED", Data: map[string]any{"snapshot": current}}
		}
		var resolvedIDs []string
		if u.itemIDsProvided {
			var ok bool
			resolvedIDs, ok = resolveAuthorItemIDs(current, nextRowCount, u.itemIDs, u.itemIDsInvalid)
			if !ok {
				return Document{}, &AuthorUpdateRejection{Code: "PROGRESS_REVISION_CHANGED", Data: map[string]any{"snapshot": current}}
			}
		} else {
			resolvedIDs = mintItemIDs(u.content)
		}
		if code, data, ok := ValidateAuthorProgress(current.Content, u.content); !ok {
			return Document{}, &AuthorUpdateRejection{Code: code, Data: data}
		}
		return Document{Content: u.content, ItemIDs: resolvedIDs}, nil
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
	result.Updated = previous.Content != snapshot.Content || !slices.Equal(previous.ItemIDs, snapshot.ItemIDs)
	transitions := ClassifyTransitions(previous, snapshot)
	result.StateChanged = transitions.Changed
	result.DeliverableClosed = transitions.Closed
	if result.Updated {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: root, Before: previous, After: snapshot})
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

func resolveAuthorItemIDs(current Snapshot, nextRowCount int, requested []string, invalid bool) ([]string, bool) {
	if invalid || len(requested) != nextRowCount {
		return nil, false
	}
	known := make(map[string]struct{}, len(current.ItemIDs))
	for _, id := range current.ItemIDs {
		if id != "" {
			known[id] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(requested))
	resolved := make([]string, nextRowCount)
	for i, raw := range requested {
		id := strings.TrimSpace(raw)
		if id == "" {
			resolved[i] = uuid.NewString()
			continue
		}
		if _, ok := known[id]; !ok {
			return nil, false
		}
		if _, dup := seen[id]; dup {
			return nil, false
		}
		seen[id] = struct{}{}
		resolved[i] = id
	}
	return resolved, true
}
