package progress

import (
	"context"
	"fmt"
	"strings"
)

// AuthorUpdate is the existing progress owner's prepared content. Preparation
// validates without writing; callers validate their entire envelope before Apply.
// Its private content prevents changes between validation and application.
type AuthorUpdate struct {
	content          string
	expectedRevision string
}

// AuthorUpdateRejection preserves the progress catalog's code and typed facts.
type AuthorUpdateRejection struct {
	Code string
	Data map[string]any
}

func (e *AuthorUpdateRejection) Error() string { return e.Code }

// PrepareAuthorUpdate applies the same structural policy to both command
// composition and the retained update_progress interface.
func PrepareAuthorUpdate(content string) (AuthorUpdate, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return AuthorUpdate{}, fmt.Errorf("content is required")
	}
	if code, data, ok := validateAuthorProgressStructure(content); !ok {
		return AuthorUpdate{}, &AuthorUpdateRejection{Code: code, Data: data}
	}
	return AuthorUpdate{content: content}, nil
}

// AuthorUpdateResult is a progress-owner outcome, independent of program success.
type AuthorUpdateResult struct {
	Updated      bool     `json:"document_changed"`
	StateChanged bool     `json:"state_changed"`
	Reconciled   bool     `json:"reconciled"`
	Snapshot     Snapshot `json:"snapshot"`
}

// ExpectRevision binds a prepared replacement to an observed document.
func (u AuthorUpdate) ExpectRevision(revision string) AuthorUpdate {
	u.expectedRevision = strings.TrimSpace(revision)
	return u
}

// Apply uses the already resolved root-session scope. Progress stays committed
// if later program admission fails; this result supplies no effect authority.
func (u AuthorUpdate) Apply(ctx context.Context, store Store, root string) (AuthorUpdateResult, error) {
	if err := ctx.Err(); err != nil {
		return AuthorUpdateResult{}, err
	}
	if strings.TrimSpace(root) == "" {
		return AuthorUpdateResult{}, fmt.Errorf("session required")
	}
	if u.content == "" || store == nil {
		return AuthorUpdateResult{}, fmt.Errorf("prepared progress and store required")
	}
	var result AuthorUpdateResult
	var previous string
	snapshot, err := store.Update(ctx, root, func(current Snapshot) (string, error) {
		previous = current.Content
		if u.expectedRevision != "" && u.expectedRevision != current.Revision {
			return "", &AuthorUpdateRejection{Code: "PROGRESS_REVISION_CHANGED", Data: map[string]any{"revision": current.Revision, "content": current.Content}}
		}
		if code, data, ok := ValidateAuthorProgress(current.Content, u.content); !ok {
			return "", &AuthorUpdateRejection{Code: code, Data: data}
		}
		result.Updated = current.Content != u.content
		result.StateChanged = StateTransitions(current.Content, u.content)
		result.Reconciled = Reconciles(current.Content, u.content)
		return u.content, nil
	})
	result.Snapshot = snapshot
	if err != nil {
		return result, err
	}
	if result.Updated {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: root, Prev: previous})
	}
	return result, nil
}
