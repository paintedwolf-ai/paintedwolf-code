package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

// FileChange binds a display preview to the resolved mutation paths.
type FileChange struct {
	Preview  api.ApprovalFileChange
	Path     string
	FromPath string
}

// FileChangeReviewer runs the ordinary approval gate with prepared file evidence.
type FileChangeReviewer func(context.Context, []FileChange) error

// ReviewFileChanges authorizes prepared changes before they reach their destinations.
func (tc ToolContext) ReviewFileChanges(ctx context.Context, changes ...FileChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tc.FileChangeReview != nil {
		if err := tc.FileChangeReview(ctx, changes); err != nil {
			return err
		}
		return ctx.Err()
	}
	for _, change := range changes {
		if change.Preview.Target == "index" {
			continue
		}
		for _, path := range []string{change.Path, change.FromPath} {
			if _, policy := tc.AgentPolicyTarget(path); policy {
				return fmt.Errorf("file change approval is not configured")
			}
		}
	}
	return nil
}

// agentPolicyTarget classifies a change against this invocation's project roots.
func (tc ToolContext) AgentPolicyTarget(path string) (hitl.AgentPolicyTarget, bool) {
	return hitl.AgentPolicyTargetFor(path, HostWriteRoot(tc), ConfineRootsForAction(tc)...)
}

// contentReviews carries one invocation's content decisions. Keys hold the
// reviewed text with managed values echoed as references, the same text a
// native tool's preview carries, so review and gate hash to one key.
type contentReviews struct {
	mu sync.Mutex
	// decided maps a reviewed proposal to the final bytes the person approved.
	decided map[contentReviewKey]string
	// covered holds approved final bytes the gate releases exactly once.
	covered map[contentReviewKey]bool
}

type contentReviewKey struct {
	path          string
	before, after [32]byte
}

func contentReview(path, before, after string) contentReviewKey {
	return contentReviewKey{fspath.CanonicalPath(path), sha256.Sum256([]byte(before)), sha256.Sum256([]byte(after))}
}

// ContentDecision returns the final bytes already approved for this exact
// proposal in the invocation and covers them at the gate again.
func (tc ToolContext) ContentDecision(path, before, proposed string) (string, bool) {
	r := tc.contentReviews
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	final, ok := r.decided[contentReview(path, before, proposed)]
	if !ok {
		return "", false
	}
	r.cover(path, before, final)
	return final, true
}

// RecordContentApproval records the final bytes a content checkpoint approved
// for a proposal. The gate releases exactly those bytes once.
func (tc ToolContext) RecordContentApproval(path, before, proposed, final string) {
	r := tc.contentReviews
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.decided == nil {
		r.decided = make(map[contentReviewKey]string)
	}
	r.decided[contentReview(path, before, proposed)] = final
	r.cover(path, before, final)
}

func (r *contentReviews) cover(path, before, final string) {
	if r.covered == nil {
		r.covered = make(map[contentReviewKey]bool)
	}
	r.covered[contentReview(path, before, final)] = true
}

func (r *contentReviews) consume(changes []FileChange) bool {
	if r == nil || len(changes) == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]contentReviewKey, 0, len(changes))
	for _, change := range changes {
		p := change.Preview
		if p.Target == "index" || change.FromPath != "" || p.PreviewNote != "" || (p.Operation != "write" && p.Operation != "create") {
			return false
		}
		key := contentReview(change.Path, p.Before, p.After)
		if !r.covered[key] {
			return false
		}
		keys = append(keys, key)
	}
	for _, key := range keys {
		delete(r.covered, key)
	}
	return true
}

// ConsumeContentApproval releases the exact bytes reviewed in this invocation once.
func (tc ToolContext) ConsumeContentApproval(changes []FileChange) bool {
	return tc.contentReviews.consume(changes)
}
