package confine

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/ingestion"
)

// UntrustedIngestionSource reads session ingestion evidence.
type UntrustedIngestionSource interface {
	// SessionIngestedUntrusted reads the root chat's state.
	SessionIngestedUntrusted(ctx context.Context, chatSessionID string) bool
}

var (
	untrustedIngestionMu  sync.RWMutex
	untrustedIngestionSrc *ingestionRegistration
)

type ingestionRegistration struct {
	source UntrustedIngestionSource
}

// SetUntrustedIngestionSource installs the pre-dial reader and returns its release.
func SetUntrustedIngestionSource(src UntrustedIngestionSource) func() {
	registration := &ingestionRegistration{source: src}
	untrustedIngestionMu.Lock()
	untrustedIngestionSrc = registration
	untrustedIngestionMu.Unlock()
	return func() {
		untrustedIngestionMu.Lock()
		defer untrustedIngestionMu.Unlock()
		if untrustedIngestionSrc == registration {
			untrustedIngestionSrc = nil
		}
		registration.source = nil
	}
}

// retrievalDial identifies dials that produce ingestion evidence.
func retrievalDial(cmd EgressCommand) bool {
	return ingestion.IsRetrievalTool(cmd.ToolName) ||
		ingestion.IsRetrievalTool(cmd.Image)
}

// sessionIngestedUntrusted reads ingestion state from the root chat.
func sessionIngestedUntrusted(ctx context.Context, cmd EgressCommand) bool {
	untrustedIngestionMu.RLock()
	var src UntrustedIngestionSource
	if untrustedIngestionSrc != nil {
		src = untrustedIngestionSrc.source
	}
	untrustedIngestionMu.RUnlock()
	if src == nil {
		return false
	}
	chat := cmd.RootSessionID
	if chat == "" {
		chat = cmd.SessionID
	}
	if chat == "" {
		return false
	}
	return src.SessionIngestedUntrusted(ctx, chat)
}
