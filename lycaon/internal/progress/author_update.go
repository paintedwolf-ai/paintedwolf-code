package progress

import (
	"context"
	"fmt"
	"strings"
)

// AuthorUpdate is the existing progress owner's prepared content. Preparation
// validates without writing; callers validate their entire envelope before Apply.
// Its private content prevents changes between validation and application.
type AuthorUpdate struct{ content string }

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
	if code, data, ok := ValidateAuthorProgress(content); !ok {
		return AuthorUpdate{}, &AuthorUpdateRejection{Code: code, Data: data}
	}
	return AuthorUpdate{content: content}, nil
}

// AuthorUpdateResult is a progress-owner outcome, independent of program success.
type AuthorUpdateResult struct{ Updated bool }

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
	prev := store.Get(ctx, root)
	if strings.TrimSpace(prev) == u.content {
		return AuthorUpdateResult{}, nil
	}
	if err := store.Set(root, u.content); err != nil {
		return AuthorUpdateResult{}, fmt.Errorf("store progress: %w", err)
	}
	NotifyWriteObservers(ctx, WriteEvent{SessionID: root, Prev: prev})
	return AuthorUpdateResult{Updated: true}, nil
}
