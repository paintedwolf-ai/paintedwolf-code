package assembly

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// TurnAssemblyScratch holds ephemeral prompt assembly state for one Prompt turn.
type TurnAssemblyScratch struct {
	Iteration                  int
	PromptTurnSeq              int
	SurfaceID                  string
	PendingKickIDs             []string
	StablePrompt               string
	StablePromptKey            string
	BoardBlock                 string
	BoardKey                   string
	LastSeenSiblingNoteID      int64
	PendingSiblingNotes        []inject.SiblingNote
	ModeTransitionCauses       []surface.ModeTransitionCause
	TransitionInjectKey        string
	TransitionInjectBlock      string
	CurrentExecutionModeFamily string
	WorkspaceRoots             []map[string]any
	WorkspaceRootCount         int
	WorkspaceActivePath        string
	WorkspaceRootsLoaded       bool
	// SourceBriefBlocks holds each turn's rendered source-change brief, keyed
	// by the message that opened the turn.
	SourceBriefBlocks  map[string]string
	SourceBriefsLoaded bool
}

// sessionStableEntry caches compiled stable blocks across turns.
type sessionStableEntry struct {
	Key    string
	Prompt string
}

// SessionPromptCache tracks per-session stable prompt blocks and per-turn scratch.
type SessionPromptCache struct {
	// Bounded by session; eviction recompiles the stable block.
	stable scopedstore.LRU[*sessionStableEntry]
	// Bounded by session; eviction restarts its turn sequence.
	promptTurnSeq scopedstore.LRU[int]
	// Bounded per turn and cleared at EndTurn.
	turns scopedstore.LRU[*TurnAssemblyScratch]
	// Bounded by session and consumed at BeginTurn.
	pendingModeCauses scopedstore.LRU[[]surface.ModeTransitionCause]
}

// BeginTurn starts a turn that delivers pendingKickIDs.
func (c *SessionPromptCache) BeginTurn(sessionID string, pendingKickIDs ...string) {
	if c == nil {
		return
	}
	seq := c.nextPromptTurnSeq(sessionID)
	turn := &TurnAssemblyScratch{PromptTurnSeq: seq}
	for _, id := range pendingKickIDs {
		if id = strings.TrimSpace(id); id != "" {
			turn.PendingKickIDs = append(turn.PendingKickIDs, id)
		}
	}
	if causes := c.takePendingModeCauses(sessionID); len(causes) > 0 {
		turn.ModeTransitionCauses = causes
	}
	c.turns.Store(sessionID, turn)
}

func (c *SessionPromptCache) nextPromptTurnSeq(sessionID string) int {
	if c == nil || sessionID == "" {
		return 0
	}
	next := 1
	if prev, ok := c.promptTurnSeq.Load(sessionID); ok && prev > 0 {
		next = prev + 1
	}
	c.promptTurnSeq.Store(sessionID, next)
	return next
}

// PushModeTransitionCause records an explicit execution-mode entry for the active or next turn.
func (c *SessionPromptCache) PushModeTransitionCause(sessionID string, cause surface.ModeTransitionCause) {
	if c == nil || sessionID == "" {
		return
	}
	if turn, ok := c.turns.Load(sessionID); ok && turn != nil {
		if len(turn.ModeTransitionCauses) > 0 {
			return
		}
		turn.ModeTransitionCauses = append(turn.ModeTransitionCauses, cause)
		return
	}
	if _, loaded := c.pendingModeCauses.Load(sessionID); loaded {
		return
	}
	c.pendingModeCauses.Store(sessionID, []surface.ModeTransitionCause{cause})
}

func (c *SessionPromptCache) takePendingModeCauses(sessionID string) []surface.ModeTransitionCause {
	if c == nil || sessionID == "" {
		return nil
	}
	causes, ok := c.pendingModeCauses.LoadAndDelete(sessionID)
	if !ok || len(causes) == 0 {
		return nil
	}
	return causes
}

func (c *SessionPromptCache) EndTurn(sessionID string) {
	if c == nil {
		return
	}
	c.turns.Delete(sessionID)
}
func (c *SessionPromptCache) SetTurnSurfaceID(sessionID, surfaceID string) {
	if c == nil {
		return
	}
	turn := c.LoadTurn(sessionID)
	turn.SurfaceID = strings.TrimSpace(surfaceID)
}

// TurnSurfaceID returns the coordinator surface for the active prompt turn.
func (c *SessionPromptCache) TurnSurfaceID(sessionID string) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.LoadTurn(sessionID).SurfaceID)
}

func (c *SessionPromptCache) LoadTurn(sessionID string) *TurnAssemblyScratch {
	if c == nil {
		return &TurnAssemblyScratch{}
	}
	if turn, ok := c.turns.Load(sessionID); ok && turn != nil {
		return turn
	}
	turn := &TurnAssemblyScratch{}
	c.turns.Store(sessionID, turn)
	return turn
}

func (c *SessionPromptCache) LoadStable(sessionID, key string) (string, bool) {
	if c == nil {
		return "", false
	}
	entry, ok := c.stable.Load(sessionID)
	if !ok || entry == nil || entry.Key != key {
		return "", false
	}
	return entry.Prompt, true
}

func (c *SessionPromptCache) StoreStable(sessionID, key, prompt string) {
	if c == nil {
		return
	}
	c.stable.Store(sessionID, &sessionStableEntry{Key: key, Prompt: prompt})
}

func boardInjectFingerprint(hash, phase string) string {
	return hashString(strings.TrimSpace(hash) + "\x00" + strings.TrimSpace(phase))
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
