// Package queue holds per-session next-turn drafts.
package queue

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// Store is a concurrency-safe set of per-session drafts. The zero value is not usable;
// construct with New.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*draft
	// Immutable held-item snapshots are readable while a mutation commits to SQL.
	awaiting sync.Map // session ID -> map[item ID]struct{}
}

type draft struct {
	items        []api.QueueItem
	admissionSeq map[string]int64
	hold         bool
	sending      []string
	revision     uint64
}

var ErrRevisionConflict = errors.New("queue draft revision conflict")

// Queue mutations return typed sentinel errors.
var (
	// ErrNoDraft is a queue command against a session holding no draft at all.
	ErrNoDraft = errors.New("queue: no draft for session")
	// ErrNothingToSend is Send with an empty queue.
	ErrNothingToSend = errors.New("queue: no messages to send")
	// ErrSendPending is a second Send while a reservation is still in flight.
	ErrSendPending = errors.New("queue: send already pending")
	// ErrSendReserved is an edit refused because the head group is reserved for sending.
	ErrSendReserved = errors.New("queue: reserved messages are being sent")
	// ErrNoSendToCancel is a cancel with no reservation outstanding.
	ErrNoSendToCancel = errors.New("queue: no reserved send to cancel")
	// ErrLinkAcrossSenders is a link of items different people sent.
	ErrLinkAcrossSenders = errors.New("queue: linked messages must come from one person")
)

type RevisionConflictError struct {
	Expected uint64
	Current  uint64
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%s: expected %d, current %d", ErrRevisionConflict, e.Expected, e.Current)
}

func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

// New returns an empty draft store.
func New() *Store {
	return &Store{sessions: make(map[string]*draft)}
}

// AppendOrdered inserts a receipt at its durable admission position.
func (s *Store) AppendOrdered(sessionID, itemID, submittedBy, text string, admissionSeq int64, createdAt time.Time) api.QueueDraft {
	s.mu.Lock()
	defer s.finishMutation(sessionID)
	if itemID == "" {
		itemID = uuid.NewString()
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	d := s.ensure(sessionID)
	for _, it := range d.items {
		if it.ID == itemID {
			return d.snapshot()
		}
	}
	if admissionSeq <= 0 {
		for _, seq := range d.admissionSeq {
			if seq >= admissionSeq {
				admissionSeq = seq + 1
			}
		}
	}
	item := api.QueueItem{
		ID:          itemID,
		SubmittedBy: submittedBy,
		Text:        text,
		CreatedAt:   createdAt.UTC().Format(time.RFC3339Nano),
	}
	insertAt := len(d.items)
	for i, existing := range d.items {
		if existingSeq := d.admissionSeq[existing.ID]; existingSeq > 0 && admissionSeq < existingSeq {
			insertAt = i
			break
		}
	}
	d.items = append(d.items, api.QueueItem{})
	copy(d.items[insertAt+1:], d.items[insertAt:])
	d.items[insertAt] = item
	d.admissionSeq[itemID] = admissionSeq
	d.normalizeGroups()
	return d.bump()
}

// Has reports whether the session's draft currently holds the given item id.
func (s *Store) Has(sessionID, itemID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.sessions[sessionID]
	if !ok {
		return false
	}
	for _, it := range d.items {
		if it.ID == itemID {
			return true
		}
	}
	return false
}

// AwaitsPerson reports whether a draft item is held for editing with no Send
// reserved, the state in which TakeNextTurn drains nothing.
func (s *Store) AwaitsPerson(sessionID, itemID string) bool {
	held, ok := s.awaiting.Load(sessionID)
	if !ok {
		return false
	}
	_, ok = held.(map[string]struct{})[itemID]
	return ok
}

// finishMutation publishes committed hold state for lock-free reads from SQL events.
func (s *Store) finishMutation(sessionID string) {
	d := s.sessions[sessionID]
	if d == nil || !d.hold || len(d.sending) > 0 || len(d.items) == 0 {
		s.awaiting.Delete(sessionID)
	} else {
		ids := make(map[string]struct{}, len(d.items))
		for _, item := range d.items {
			ids[item.ID] = struct{}{}
		}
		s.awaiting.Store(sessionID, ids)
	}
	s.mu.Unlock()
}

// Snapshot returns the current draft for a session (empty, revision 0, if none exists).
func (s *Store) Snapshot(sessionID string) api.QueueDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.sessions[sessionID]
	if !ok {
		return api.QueueDraft{QueueItems: []api.QueueItem{}}
	}
	return d.snapshot()
}

// Reorder requires every current item ID exactly once.
func (s *Store) Reorder(sessionID string, expectedRevision uint64, orderedIDs []string) (api.QueueDraft, error) {
	return s.mutate(sessionID, expectedRevision, false, func(d *draft) error {
		if err := d.requireEditable(); err != nil {
			return err
		}
		if len(orderedIDs) != len(d.items) {
			return fmt.Errorf("queue: reorder must list every item exactly once")
		}
		byID := make(map[string]api.QueueItem, len(d.items))
		for _, it := range d.items {
			byID[it.ID] = it
		}
		next := make([]api.QueueItem, 0, len(orderedIDs))
		for _, id := range orderedIDs {
			it, ok := byID[id]
			if !ok {
				return fmt.Errorf("queue: unknown item %q in reorder", id)
			}
			delete(byID, id)
			next = append(next, it)
		}
		d.items = next
		d.normalizeGroups()
		return nil
	})
}

// Link assigns a shared group to the given items so they drain as one coalesced turn.
func (s *Store) Link(sessionID string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	return s.setGroup(sessionID, expectedRevision, ids, uuid.NewString())
}

// Unlink clears the group on the given items so each drains as its own turn.
func (s *Store) Unlink(sessionID string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	return s.setGroup(sessionID, expectedRevision, ids, "")
}

func (s *Store) setGroup(sessionID string, expectedRevision uint64, ids []string, group string) (api.QueueDraft, error) {
	return s.mutate(sessionID, expectedRevision, false, func(d *draft) error {
		if err := d.requireEditable(); err != nil {
			return err
		}
		want := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			want[id] = struct{}{}
		}
		senders := make(map[string]struct{}, 1)
		for id := range want {
			found := false
			for _, item := range d.items {
				if item.ID == id {
					found = true
					senders[item.SubmittedBy] = struct{}{}
					break
				}
			}
			if !found {
				return fmt.Errorf("queue: link/unlink referenced unknown items")
			}
		}
		// A linked group becomes one message, and a message has one author.
		if group != "" && len(senders) > 1 {
			return ErrLinkAcrossSenders
		}
		for i := range d.items {
			if _, ok := want[d.items[i].ID]; ok {
				d.items[i].GroupID = group
			}
		}
		return nil
	})
}

// Remove commits receipt changes before publishing the draft revision.
func (s *Store) Remove(sessionID string, expectedRevision uint64, ids []string, commit func() error) (api.QueueDraft, error) {
	if commit == nil {
		return s.Snapshot(sessionID), fmt.Errorf("queue: removal commit required")
	}
	return s.mutateCommitted(sessionID, expectedRevision, false, func(d *draft) error {
		if err := d.requireEditable(); err != nil {
			return err
		}
		want := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			want[id] = struct{}{}
		}
		next := d.items[:0:0]
		hit := 0
		for _, it := range d.items {
			if _, ok := want[it.ID]; ok {
				hit++
				continue
			}
			next = append(next, it)
		}
		if hit != len(want) {
			return fmt.Errorf("queue: remove referenced unknown items")
		}
		d.items = next
		for id := range want {
			delete(d.admissionSeq, id)
		}
		d.normalizeGroups()
		return nil
	}, commit)
}

// UpdateText commits receipt input before publishing the draft revision.
func (s *Store) UpdateText(sessionID string, expectedRevision uint64, itemID, text string, commit func() error) (api.QueueDraft, error) {
	if commit == nil {
		return s.Snapshot(sessionID), fmt.Errorf("queue: update commit required")
	}
	return s.mutateCommitted(sessionID, expectedRevision, false, func(d *draft) error {
		if err := d.requireEditable(); err != nil {
			return err
		}
		for i := range d.items {
			if d.items[i].ID == itemID {
				d.items[i].Text = text
				return nil
			}
		}
		return fmt.Errorf("queue: unknown item %q", itemID)
	}, commit)
}

// MoveFront moves items to the front without changing their relative order.
func (s *Store) MoveFront(sessionID string, expectedRevision uint64, ids []string) (api.QueueDraft, error) {
	return s.mutate(sessionID, expectedRevision, false, func(d *draft) error {
		if err := d.requireEditable(); err != nil {
			return err
		}
		want := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			want[id] = struct{}{}
		}
		front := make([]api.QueueItem, 0, len(ids))
		rest := make([]api.QueueItem, 0, len(d.items))
		hit := 0
		for _, it := range d.items {
			if _, ok := want[it.ID]; ok {
				front = append(front, it)
				hit++
				continue
			}
			rest = append(rest, it)
		}
		if hit != len(want) {
			return fmt.Errorf("queue: fire-now referenced unknown items")
		}
		d.items = append(front, rest...) //nolint:gocritic // Both slices have independent backing arrays.
		d.normalizeGroups()
		return nil
	})
}

// SetHold toggles the drain-hold flag, suppressing round-end draining while the user edits.
func (s *Store) SetHold(sessionID string, expectedRevision uint64, hold bool) (api.QueueDraft, error) {
	return s.mutate(sessionID, expectedRevision, true, func(d *draft) error {
		if err := d.requireEditable(); err != nil {
			return err
		}
		d.hold = hold
		return nil
	})
}

// RequestSend durably reserves the head group for in-turn delivery.
func (s *Store) RequestSend(
	sessionID string,
	expectedRevision uint64,
	commit func([]api.QueueItem) error,
) (api.QueueDraft, error) {
	if commit == nil {
		return s.Snapshot(sessionID), fmt.Errorf("queue: send commit required")
	}
	var reserved []api.QueueItem
	return s.mutateCommitted(sessionID, expectedRevision, false, func(d *draft) error {
		if len(d.sending) > 0 {
			return ErrSendPending
		}
		if len(d.items) == 0 {
			return ErrNothingToSend
		}
		d.hold = false
		d.sending = d.headGroupIDs()
		reserved = d.itemsByID(d.sending)
		return nil
	}, func() error { return commit(reserved) })
}

// CancelSend returns an unclaimed reservation to the editable queue.
func (s *Store) CancelSend(
	sessionID string,
	expectedRevision uint64,
	commit func([]api.QueueItem) error,
) (api.QueueDraft, error) {
	if commit == nil {
		return s.Snapshot(sessionID), fmt.Errorf("queue: cancel commit required")
	}
	var released []api.QueueItem
	return s.mutateCommitted(sessionID, expectedRevision, false, func(d *draft) error {
		if len(d.sending) == 0 {
			return ErrNoSendToCancel
		}
		released = d.itemsByID(d.sending)
		d.sending = nil
		return nil
	}, func() error { return commit(released) })
}

// TakeSendTurn commits and removes the exact group reserved by RequestSend.
func (s *Store) TakeSendTurn(sessionID string, commit func([]api.QueueItem) error) (items []api.QueueItem, ok bool, err error) {
	if commit == nil {
		return nil, false, fmt.Errorf("queue: send commit required")
	}
	s.mu.Lock()
	defer s.finishMutation(sessionID)
	d, exists := s.sessions[sessionID]
	if !exists || len(d.sending) == 0 {
		return nil, false, nil
	}
	return d.takeIDs(commit)
}

// TakeNextTurn commits receipts before removing the draft turn.
func (s *Store) TakeNextTurn(sessionID string, commit func([]api.QueueItem) error) (items []api.QueueItem, ok bool, err error) {
	if commit == nil {
		return nil, false, fmt.Errorf("queue: turn commit required")
	}
	s.mu.Lock()
	defer s.finishMutation(sessionID)
	d, exists := s.sessions[sessionID]
	if !exists || len(d.items) == 0 || (d.hold && len(d.sending) == 0) {
		return nil, false, nil
	}
	if len(d.sending) > 0 {
		return d.takeIDs(commit)
	}
	group := d.items[0].GroupID
	keep := make([]api.QueueItem, 0, len(d.items))
	for _, it := range d.items {
		if group != "" && it.GroupID == group {
			items = append(items, it)
			continue
		}
		if group == "" && it.ID == d.items[0].ID {
			items = append(items, it)
			continue
		}
		keep = append(keep, it)
	}
	if err := commit(items); err != nil {
		return nil, false, err
	}
	d.items = keep
	for _, item := range items {
		delete(d.admissionSeq, item.ID)
	}
	d.revision++
	return items, true, nil
}

// CancelAll empties a live draft and advances its observer revision.
func (s *Store) CancelAll(sessionID string) api.QueueDraft {
	s.mu.Lock()
	defer s.finishMutation(sessionID)
	d := s.ensure(sessionID)
	d.items = nil
	d.admissionSeq = make(map[string]int64)
	d.hold = false
	d.sending = nil
	d.revision++
	return d.snapshot()
}

// Clear discards a session's draft (session deletion / teardown).
func (s *Store) Clear(sessionID string) {
	s.mu.Lock()
	defer s.finishMutation(sessionID)
	delete(s.sessions, sessionID)
}

func (s *Store) ensure(sessionID string) *draft {
	d, ok := s.sessions[sessionID]
	if !ok {
		d = &draft{admissionSeq: make(map[string]int64)}
		s.sessions[sessionID] = d
	}
	return d
}

func (s *Store) mutate(sessionID string, expectedRevision uint64, create bool, apply func(*draft) error) (api.QueueDraft, error) {
	return s.mutateCommitted(sessionID, expectedRevision, create, apply, nil)
}

func (s *Store) mutateCommitted(sessionID string, expectedRevision uint64, create bool, apply func(*draft) error, commit func() error) (api.QueueDraft, error) {
	s.mu.Lock()
	defer s.finishMutation(sessionID)
	current, ok := s.sessions[sessionID]
	if !ok {
		if !create {
			return api.QueueDraft{QueueItems: []api.QueueItem{}}, ErrNoDraft
		}
		current = &draft{}
	}
	if current.revision != expectedRevision {
		return current.snapshot(), &RevisionConflictError{Expected: expectedRevision, Current: current.revision}
	}
	next := current.clone()
	if err := apply(next); err != nil {
		return current.snapshot(), err
	}
	if commit != nil {
		if err := commit(); err != nil {
			return current.snapshot(), err
		}
	}
	next.revision++
	s.sessions[sessionID] = next
	return next.snapshot(), nil
}

func (d *draft) clone() *draft {
	admissionSeq := make(map[string]int64, len(d.admissionSeq))
	for id, seq := range d.admissionSeq {
		admissionSeq[id] = seq
	}
	return &draft{
		items: append([]api.QueueItem(nil), d.items...), admissionSeq: admissionSeq,
		hold: d.hold, sending: append([]string(nil), d.sending...), revision: d.revision,
	}
}

func (d *draft) requireEditable() error {
	if len(d.sending) > 0 {
		return ErrSendReserved
	}
	return nil
}

func (d *draft) headGroupIDs() []string {
	if len(d.items) == 0 {
		return nil
	}
	group := d.items[0].GroupID
	ids := make([]string, 0, 1)
	for _, item := range d.items {
		if (group == "" && item.ID == d.items[0].ID) || (group != "" && item.GroupID == group) {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func (d *draft) itemsByID(ids []string) []api.QueueItem {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	items := make([]api.QueueItem, 0, len(ids))
	for _, item := range d.items {
		if _, ok := want[item.ID]; ok {
			items = append(items, item)
		}
	}
	return items
}

func (d *draft) takeIDs(commit func([]api.QueueItem) error) ([]api.QueueItem, bool, error) {
	want := make(map[string]struct{}, len(d.sending))
	for _, id := range d.sending {
		want[id] = struct{}{}
	}
	items := make([]api.QueueItem, 0, len(want))
	keep := make([]api.QueueItem, 0, len(d.items)-len(want))
	for _, item := range d.items {
		if _, ok := want[item.ID]; ok {
			items = append(items, item)
			continue
		}
		keep = append(keep, item)
	}
	if len(items) != len(want) {
		return nil, false, fmt.Errorf("queue: reserved send group is incomplete")
	}
	if err := commit(items); err != nil {
		return nil, false, err
	}
	d.items = keep
	for _, item := range items {
		delete(d.admissionSeq, item.ID)
	}
	d.sending = nil
	d.revision++
	return items, true, nil
}

// normalizeGroups keeps only contiguous groups with at least two members.
func (d *draft) normalizeGroups() {
	count := make(map[string]int)
	lastIdx := make(map[string]int)
	contiguous := make(map[string]bool)
	for i, it := range d.items {
		if it.GroupID == "" {
			continue
		}
		if n := count[it.GroupID]; n > 0 && lastIdx[it.GroupID] != i-1 {
			contiguous[it.GroupID] = false
		} else if n == 0 {
			contiguous[it.GroupID] = true
		}
		count[it.GroupID]++
		lastIdx[it.GroupID] = i
	}
	for i := range d.items {
		g := d.items[i].GroupID
		if g != "" && (count[g] < 2 || !contiguous[g]) {
			d.items[i].GroupID = ""
		}
	}
}

func (d *draft) snapshot() api.QueueDraft {
	items := make([]api.QueueItem, len(d.items))
	copy(items, d.items)
	return api.QueueDraft{QueueItems: items, Hold: d.hold, Sending: len(d.sending) > 0, Revision: d.revision}
}

// bump increments the revision and returns the post-mutation snapshot.
func (d *draft) bump() api.QueueDraft {
	d.revision++
	return d.snapshot()
}
