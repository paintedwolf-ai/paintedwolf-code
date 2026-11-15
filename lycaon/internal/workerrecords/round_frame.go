package workerrecords

import (
	"container/list"
	"context"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
)

// Workspace preparations a later round's frame names.
const (
	WorkspaceRetained  = "retained"
	WorkspaceRefreshed = "refreshed"
	WorkspaceReset     = "reset"
)

// Bounds keep a worker's round frame and its resident cache bounded across
// long conversations and many workers.
const (
	MaxFrameEarlierResults   = 64
	MaxFrameInstructions     = 64
	MaxFrameInstructionBytes = 16 * 1024
	MaxCachedRoundFrames     = 64
)

// RoundFrame is what every request of a worker's running round carries: its
// standing brief, the instructions the round received, and on a later round
// how its workspace was prepared and what became of its last changes.
type RoundFrame struct {
	Ordinal      int64
	Brief        string
	Instructions []Message
	// Workspace is retained, refreshed or reset; empty on a first round.
	Workspace string
	// PriorChange is landed, rejected, shelved or open for the latest
	// earlier round that changed files; empty when none did.
	PriorChange string
	// EarlierResults are the handles of the worker's settled rounds, oldest
	// first: the results a later report may cite.
	EarlierResults []string
}

func cloneRoundFrame(f RoundFrame) RoundFrame {
	f.Instructions = append([]Message(nil), f.Instructions...)
	f.EarlierResults = append([]string(nil), f.EarlierResults...)
	return f
}

type roundFrameRevKey struct {
	WorkerID       string
	UpdatedAt      string
	Brief          string
	NextRoundOrd   int64
	NextMessageSeq int64
	RoundOrdinal   int64
	PrepGeneration int64
	PrepCreatedAt  string
	PriorResultID  string
	PriorSettledAt string
	PriorChange    string
}

type roundFrameCacheEntry struct {
	workerID string
	key      roundFrameRevKey
	frame    RoundFrame
}

type roundFrameCache struct {
	mu      sync.Mutex
	maxSize int
	lru     *list.List
	items   map[string]*list.Element
	hits    uint64
	misses  uint64
}

func newRoundFrameCache(maxSize int) *roundFrameCache {
	if maxSize <= 0 {
		maxSize = MaxCachedRoundFrames
	}
	return &roundFrameCache{
		maxSize: maxSize,
		lru:     list.New(),
		items:   make(map[string]*list.Element, maxSize),
	}
}

func (c *roundFrameCache) get(key roundFrameRevKey) (RoundFrame, bool) {
	if c == nil {
		return RoundFrame{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key.WorkerID]
	if !ok {
		c.misses++
		return RoundFrame{}, false
	}
	entry := el.Value.(*roundFrameCacheEntry)
	if entry.key != key {
		c.lru.Remove(el)
		delete(c.items, key.WorkerID)
		c.misses++
		return RoundFrame{}, false
	}
	c.lru.MoveToFront(el)
	c.hits++
	return cloneRoundFrame(entry.frame), true
}

func (c *roundFrameCache) put(key roundFrameRevKey, frame RoundFrame) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := cloneRoundFrame(frame)
	if el, ok := c.items[key.WorkerID]; ok {
		entry := el.Value.(*roundFrameCacheEntry)
		entry.key = key
		entry.frame = stored
		c.lru.MoveToFront(el)
		return
	}
	el := c.lru.PushFront(&roundFrameCacheEntry{workerID: key.WorkerID, key: key, frame: stored})
	c.items[key.WorkerID] = el
	for c.lru.Len() > c.maxSize {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.items, oldest.Value.(*roundFrameCacheEntry).workerID)
	}
}

func (c *roundFrameCache) delete(workerID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[workerID]; ok {
		c.lru.Remove(el)
		delete(c.items, workerID)
	}
}

// CachedRoundFrameCount reports how many worker round frames reside in cache.
func (s *Store) CachedRoundFrameCount() int {
	if s == nil || s.frameCache == nil {
		return 0
	}
	s.frameCache.mu.Lock()
	defer s.frameCache.mu.Unlock()
	return s.frameCache.lru.Len()
}

// RoundFrameCacheStats reports round-frame cache hits and misses.
func (s *Store) RoundFrameCacheStats() (hits, misses uint64) {
	if s == nil || s.frameCache == nil {
		return 0, 0
	}
	s.frameCache.mu.Lock()
	defer s.frameCache.mu.Unlock()
	return s.frameCache.hits, s.frameCache.misses
}

// RoundFrame reads the frame of the running round of the worker whose child
// session the actor runs in. ok is false when it has no running round.
func (s *Store) RoundFrame(ctx context.Context, actor Actor) (RoundFrame, bool, error) {
	w, err := s.Self(ctx, actor)
	if err != nil {
		return RoundFrame{}, false, err
	}
	current, err := s.q.GetUnsettledWorkerRound(ctx, w.ID)
	if db.IsNoRows(err) {
		s.frameCache.delete(w.ID)
		return RoundFrame{}, false, nil
	}
	if err != nil {
		return RoundFrame{}, false, err
	}
	rev := roundFrameRevKey{
		WorkerID:       w.ID,
		UpdatedAt:      w.UpdatedAt,
		Brief:          w.Brief,
		NextRoundOrd:   w.NextRoundOrdinal,
		NextMessageSeq: w.NextMessageSeq,
		RoundOrdinal:   current.Ordinal,
	}
	if current.Ordinal > 1 {
		prior, pErr := s.q.GetLatestSettledChangedWorkerRoundBefore(ctx, db.GetLatestSettledChangedWorkerRoundBeforeParams{
			WorkerID: w.ID, BeforeOrdinal: current.Ordinal,
		})
		if pErr != nil && !db.IsNoRows(pErr) {
			return RoundFrame{}, false, pErr
		}
		if pErr == nil {
			rev.PriorResultID = prior.ResultID
			rev.PriorSettledAt = prior.SettledAt
			rev.PriorChange = prior.ChangeState
		}
		prep, prepErr := s.q.GetLatestWorkerWorkspacePreparation(ctx, w.ID)
		if prepErr != nil && !db.IsNoRows(prepErr) {
			return RoundFrame{}, false, prepErr
		}
		if prepErr == nil {
			rev.PrepGeneration = prep.Generation
			rev.PrepCreatedAt = prep.CreatedAt
		}
	}
	if cached, ok := s.frameCache.get(rev); ok {
		return cached, true, nil
	}
	frame := RoundFrame{Ordinal: current.Ordinal, Brief: w.Brief}
	rows, err := s.q.ListRoundInstructionsAfter(ctx, db.ListRoundInstructionsAfterParams{
		WorkerID: w.ID, RoundOrdinal: nullInt(current.Ordinal), AfterSeq: 0,
	})
	if err != nil {
		return RoundFrame{}, false, err
	}
	frame.Instructions = boundRoundInstructions(rows)
	if current.Ordinal > 1 {
		ids, err := s.q.ListRecentSettledWorkerResultIDsBefore(ctx, db.ListRecentSettledWorkerResultIDsBeforeParams{
			WorkerID: w.ID, BeforeOrdinal: current.Ordinal, PageLimit: MaxFrameEarlierResults,
		})
		if err != nil {
			return RoundFrame{}, false, err
		}
		slices.Reverse(ids)
		frame.EarlierResults = ids
		if rev.PriorResultID != "" {
			frame.PriorChange = "open"
			if rev.PriorChange != "" {
				frame.PriorChange = rev.PriorChange
			}
		}
		frame.Workspace = WorkspaceRetained
		// A preparation made after the earlier change settled was this round's.
		if rev.PrepCreatedAt != "" && rev.PriorSettledAt != "" && rev.PrepCreatedAt > rev.PriorSettledAt {
			switch frame.PriorChange {
			case ChangeLanded:
				frame.Workspace = WorkspaceRefreshed
			case ChangeRejected:
				frame.Workspace = WorkspaceReset
			}
		}
	}
	s.frameCache.put(rev, frame)
	return frame, true, nil
}

// boundRoundInstructions keeps the round's starting instruction and the newest
// instructions within count and byte limits so extreme instruction bursts
// cannot overflow the frame.
func boundRoundInstructions(rows []db.WorkerMessages) []Message {
	if len(rows) == 0 {
		return nil
	}
	msgs := make([]Message, 0, len(rows))
	for _, r := range rows {
		msgs = append(msgs, messageFromRow(r))
	}
	if len(msgs) > MaxFrameInstructions {
		tail := msgs[len(msgs)-(MaxFrameInstructions-1):]
		msgs = append([]Message{msgs[0]}, tail...)
	}
	if len(msgs) <= 1 {
		return msgs
	}
	first := msgs[0]
	budget := MaxFrameInstructionBytes - messageByteLen(first)
	if budget <= 0 {
		return []Message{first, msgs[len(msgs)-1]}
	}
	var tail []Message
	used := 0
	for i := len(msgs) - 1; i >= 1; i-- {
		sz := messageByteLen(msgs[i])
		if len(tail) > 0 && used+sz > budget {
			break
		}
		tail = append(tail, msgs[i])
		used += sz
	}
	slices.Reverse(tail)
	return append([]Message{first}, tail...)
}

// EarlierResults lists up to MaxFrameEarlierResults handles of the settled
// rounds of the worker whose child session the actor runs in, oldest first.
func (s *Store) EarlierResults(ctx context.Context, actor Actor) ([]string, error) {
	w, err := s.Self(ctx, actor)
	if err != nil {
		return nil, err
	}
	ids, err := s.q.ListRecentSettledWorkerResultIDsBefore(ctx, db.ListRecentSettledWorkerResultIDsBeforeParams{
		WorkerID: w.ID, BeforeOrdinal: w.NextRoundOrdinal, PageLimit: MaxFrameEarlierResults,
	})
	if err != nil {
		return nil, err
	}
	slices.Reverse(ids)
	return ids, nil
}
