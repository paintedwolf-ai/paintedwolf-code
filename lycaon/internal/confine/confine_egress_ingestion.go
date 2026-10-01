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
	untrustedIngestionSrc UntrustedIngestionSource
)

// SetUntrustedIngestionSource installs the pre-dial ingestion reader.
func SetUntrustedIngestionSource(src UntrustedIngestionSource) {
	untrustedIngestionMu.Lock()
	untrustedIngestionSrc = src
	untrustedIngestionMu.Unlock()
}

// retrievalDial identifies dials that produce ingestion evidence.
func retrievalDial(cmd EgressCommand) bool {
	return ingestion.IsRetrievalTool(cmd.ToolName) ||
		ingestion.IsRetrievalTool(cmd.Image)
}

// sessionIngestedUntrusted reads ingestion state from the root chat.
func sessionIngestedUntrusted(ctx context.Context, cmd EgressCommand) bool {
	untrustedIngestionMu.RLock()
	src := untrustedIngestionSrc
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
