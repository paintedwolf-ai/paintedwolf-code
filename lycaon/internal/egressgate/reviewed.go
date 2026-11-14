package egressgate

import (
	"context"
	"strings"
	"sync"
)

// reviewedRecipients are the recipients the person approved this invocation's
// payload for. A hop to one of them carries that consent to its connection.
type reviewedRecipients struct {
	mu  sync.Mutex
	ids map[string]bool
}

type reviewedContextKey struct{}

// WithReviewedRecipients starts the invocation's record of approved recipients.
func WithReviewedRecipients(ctx context.Context) context.Context {
	return context.WithValue(ctx, reviewedContextKey{}, &reviewedRecipients{ids: map[string]bool{}})
}

// ApproveRecipient records that the person approved sending this
// invocation's payload to the recipient.
func ApproveRecipient(ctx context.Context, recipientID string) {
	reviewed, _ := ctx.Value(reviewedContextKey{}).(*reviewedRecipients)
	recipientID = strings.TrimSpace(recipientID)
	if reviewed == nil || recipientID == "" {
		return
	}
	reviewed.mu.Lock()
	reviewed.ids[recipientID] = true
	reviewed.mu.Unlock()
}

func recipientReviewed(ctx context.Context, recipientID string) bool {
	reviewed, _ := ctx.Value(reviewedContextKey{}).(*reviewedRecipients)
	if reviewed == nil || recipientID == "" {
		return false
	}
	reviewed.mu.Lock()
	defer reviewed.mu.Unlock()
	return reviewed.ids[recipientID]
}
