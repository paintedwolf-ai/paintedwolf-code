package progress

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// AuthorUpdate validates a complete replacement before publication.
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

// AuthorUpdateResult is a progress-owner outcome, independent of program success.
type AuthorUpdateResult struct {
	Updated           bool     `json:"document_changed"`
	StateChanged      bool     `json:"state_changed"`
	DeliverableClosed bool     `json:"deliverable_closed"`
	Snapshot          Snapshot `json:"snapshot"`
}

// ExpectRevision binds a prepared replacement to an observed document.
func (u AuthorUpdate) ExpectRevision(revision string) AuthorUpdate {
	u.expectedRevision = strings.TrimSpace(revision)
	return u
}

// Apply checks the observed revision and publishes one atomic replacement.
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
			return "", &AuthorUpdateRejection{Code: "PROGRESS_REVISION_CHANGED", Data: map[string]any{"snapshot": current}}
		}
		if code, data, ok := ValidateAuthorProgress(current.Content, u.content); !ok {
			return "", &AuthorUpdateRejection{Code: code, Data: data}
		}
		result.Updated = current.Content != u.content
		result.StateChanged = StateTransitions(current.Content, u.content)
		result.DeliverableClosed = Reconciles(current.Content, u.content)
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
