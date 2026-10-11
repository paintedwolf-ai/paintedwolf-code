package protection

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// SessionUIProvider is the events/API chrome surface ComputeSessionUI implements.
type SessionUIProvider interface {
	ComputeSessionUI(ctx context.Context, sessionID string) (*api.SessionUiState, error)
}

// UIWithProtection wraps an inner SessionUIProvider and attaches protection chrome.
type UIWithProtection struct {
	Inner      SessionUIProvider
	Protection *Service
}

// ComputeSessionUI implements SessionUIProvider.
func (w UIWithProtection) ComputeSessionUI(ctx context.Context, sessionID string) (*api.SessionUiState, error) {
	var ui *api.SessionUiState
	if w.Inner != nil {
		inner, err := w.Inner.ComputeSessionUI(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		ui = inner
	}
	var prot *api.SessionProtectionState
	if w.Protection != nil {
		prot = w.Protection.ProtectionStateForSession(sessionID)
	} else {
		prot = BuildProtectionState(sessionID, nil)
	}
	return ApplyProtection(ui, prot), nil
}
