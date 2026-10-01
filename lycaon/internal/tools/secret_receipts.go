package tools

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// maxContestAsksPerChat caps agent-raised unredact cards per session tree.
const maxContestAsksPerChat = 5

// secretReceipt records one redacted send the agent may contest.
type secretReceipt struct {
	rootSessionID string
	destinationID string
	fingerprints  []secretmatch.SecretFingerprint
}

// secretReceiptRuntime holds single-use redaction receipts per session tree.
type secretReceiptRuntime struct {
	mu       sync.Mutex
	byToken  map[string]secretReceipt
	contests map[string]int
}

func newSecretReceiptRuntime() *secretReceiptRuntime {
	return &secretReceiptRuntime{byToken: map[string]secretReceipt{}, contests: map[string]int{}}
}

// Mint records a redacted send and returns the contest token.
func (r *secretReceiptRuntime) Mint(rootSessionID, destinationID string, fingerprints []secretmatch.SecretFingerprint) string {
	if r == nil || strings.TrimSpace(rootSessionID) == "" {
		return ""
	}
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := hex.EncodeToString(raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byToken[token] = secretReceipt{
		rootSessionID: rootSessionID,
		destinationID: destinationID,
		fingerprints:  append([]secretmatch.SecretFingerprint(nil), fingerprints...),
	}
	return token
}

// Consume redeems a token once. It fails for another session's token, a spent
// token, or a session past the contest cap — the caller proceeds redacted.
func (r *secretReceiptRuntime) Consume(rootSessionID, token string) (secretReceipt, bool) {
	if r == nil {
		return secretReceipt{}, false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return secretReceipt{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt, ok := r.byToken[token]
	if !ok || receipt.rootSessionID != strings.TrimSpace(rootSessionID) {
		return secretReceipt{}, false
	}
	if r.contests[receipt.rootSessionID] >= maxContestAsksPerChat {
		return secretReceipt{}, false
	}
	delete(r.byToken, token)
	r.contests[receipt.rootSessionID]++
	return receipt, true
}

// secretReceipts returns the executor's receipt store, creating it on first use.
func (e *DefaultToolExecutor) secretReceipts() *secretReceiptRuntime {
	if e == nil {
		return nil
	}
	e.secretReceiptOnce.Do(func() { e.secretReceiptRT = newSecretReceiptRuntime() })
	return e.secretReceiptRT
}

// Forget drops a session tree's receipts and contest budget.
func (r *secretReceiptRuntime) Forget(rootSessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for token, receipt := range r.byToken {
		if receipt.rootSessionID == rootSessionID {
			delete(r.byToken, token)
		}
	}
	delete(r.contests, rootSessionID)
}
