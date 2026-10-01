package projectignore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

var ErrProtected = errors.New("protected credentials cannot be ignored")
var ErrRootNotFound = errors.New("the selected project folder is no longer attached")
var ErrReviewUnavailable = errors.New("the secret review is no longer available")
var ErrEntryNotFound = errors.New("that declaration is no longer in this project's ignore file")

// Add records an explicit human declaration; its id makes retries idempotent.
func (s *SecretService) Add(ctx context.Context, projectID, rootID string, entry SecretEntry) error {
	if s == nil || s.Roots == nil {
		return ErrUnavailable
	}
	if s.Trusted != nil && !s.Trusted(ctx, projectID) {
		return ErrUntrusted
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	if _, err := uuid.Parse(entry.ID); err != nil {
		return ErrInvalid
	}
	roots, err := s.Roots(ctx, projectID)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if root.ID != rootID {
			continue
		}
		err = Edit(root.Path, "secrets", func(list *yaml.Node) error {
			if s.Trusted != nil && !s.Trusted(ctx, projectID) {
				return ErrUntrusted
			}
			if s.Protected != nil && s.Protected(ctx, projectID, entry.Value) {
				return ErrProtected
			}
			found := false
			for _, node := range list.Content {
				var existing SecretEntry
				if err := node.Decode(&existing); err != nil && existing.ID != entry.ID {
					continue
				}
				if existing.ID != entry.ID {
					continue
				}
				raw, err := yaml.Marshal(node)
				if err != nil || config.DecodeYAML(raw, &existing) != nil || found || existing != entry {
					return ErrConflict
				}
				found = true
			}
			if found {
				return nil
			}
			var node yaml.Node
			if err := node.Encode(entry); err != nil {
				return err
			}
			list.Content = append(list.Content, &node)
			return nil
		})
		if err == nil && s.Changed != nil {
			s.Changed(ctx, projectID)
		}
		return err
	}
	return ErrRootNotFound
}

// Remove withdraws one declaration by the key the listing publishes. Withdrawing
// an exception only narrows what this project ignores, so it stays available
// while the project's scanning trust surface is disabled.
func (s *SecretService) Remove(ctx context.Context, projectID, rootID, key string) error {
	if s == nil || s.Roots == nil {
		return ErrUnavailable
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrEntryNotFound
	}
	roots, err := s.Roots(ctx, projectID)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if root.ID != rootID {
			continue
		}
		err = Edit(root.Path, "secrets", func(list *yaml.Node) error {
			kept := make([]*yaml.Node, 0, len(list.Content))
			for _, node := range list.Content {
				if secretNodeKey(node) != key {
					kept = append(kept, node)
				}
			}
			if len(kept) == len(list.Content) {
				return ErrEntryNotFound
			}
			list.Content = kept
			return nil
		})
		if err == nil && s.Changed != nil {
			s.Changed(ctx, projectID)
		}
		return err
	}
	return ErrRootNotFound
}

// secretNodeKey reads the listing key of one declaration. A refused entry has no
// key, so it stays in place and remains addressable only in the file itself.
func secretNodeKey(node *yaml.Node) string {
	var entry SecretEntry
	raw, err := yaml.Marshal(node)
	if err != nil || config.DecodeYAML(raw, &entry) != nil || entry.Validate() != nil {
		return ""
	}
	return entry.Key()
}

const (
	secretReviewLifetime = 30 * time.Minute
	secretReviewLimit    = 128
)

type reviewCandidate struct {
	projectID string
	value     string
	deadline  time.Time
	ctx       context.Context
}

// SecretReviews holds plaintext only while its originating approval is live.
type SecretReviews struct {
	mu         sync.Mutex
	candidates map[string]reviewCandidate
}

func (r *SecretReviews) Offer(ctx context.Context, projectID, value string) (string, func()) {
	if projectID == "" || (SecretEntry{Value: value, Reason: "review"}).Validate() != nil || ctx.Err() != nil {
		return "", func() {}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.candidates == nil {
		r.candidates = make(map[string]reviewCandidate)
	}
	for id, candidate := range r.candidates {
		if candidate.ctx.Err() != nil || time.Now().After(candidate.deadline) {
			delete(r.candidates, id)
		}
	}
	if len(r.candidates) >= secretReviewLimit {
		return "", func() {}
	}
	id := uuid.NewString()
	r.candidates[id] = reviewCandidate{projectID: projectID, value: value, deadline: time.Now().Add(secretReviewLifetime), ctx: ctx}
	clear := func() { r.mu.Lock(); delete(r.candidates, id); r.mu.Unlock() }
	timer := time.AfterFunc(secretReviewLifetime, clear)
	stopCancellation := context.AfterFunc(ctx, clear)
	return id, func() { timer.Stop(); stopCancellation(); clear() }
}

func (r *SecretReviews) Value(projectID, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	candidate, ok := r.candidates[id]
	if !ok || candidate.projectID != projectID {
		return "", ErrReviewUnavailable
	}
	if candidate.ctx.Err() != nil || time.Now().After(candidate.deadline) {
		delete(r.candidates, id)
		return "", ErrReviewUnavailable
	}
	return candidate.value, nil
}
