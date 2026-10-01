package session

import (
	"github.com/lycaon/lycaon/internal/scopedstore"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// PlanToolStash stores compact spec-plan reject blocks keyed by tool_call_id for LLM replay.
type PlanToolStash struct {
	mu sync.Mutex
	// Bounded: sessionID -> toolCallID -> block. The stash only ever re-inflates
	// a reject block the model already saw, so eviction costs one replayed tool
	// result losing its compact form, never a changed decision.
	blocks scopedstore.LRU[map[string]string]
}

// NewPlanToolStash constructs an empty per-session stash.
func NewPlanToolStash() *PlanToolStash {
	return &PlanToolStash{}
}

// Put records a reject block when a spec posture deny is surfaced to the model.
func (s *PlanToolStash) Put(sessionID, toolCallID, code, block string) {
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	block = strings.TrimSpace(block)
	code = strings.TrimSpace(code)
	if s == nil || sessionID == "" || toolCallID == "" || block == "" {
		return
	}
	if !strings.HasPrefix(code, "SPEC_") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.blocks.Load(sessionID)
	if !ok || sess == nil {
		sess = make(map[string]string)
	}
	sess[toolCallID] = block
	s.blocks.Store(sessionID, sess)
}

// Get returns a stashed block for the session and tool call id.
func (s *PlanToolStash) Get(sessionID, toolCallID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	if s == nil || sessionID == "" || toolCallID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.blocks.Load(sessionID)
	if !ok {
		return "", false
	}
	block, ok := sess[toolCallID]
	return block, ok && strings.TrimSpace(block) != ""
}

// EnrichHistoryToolPartsForAgent reinjects stashed compact blocks when tool content was dropped.
func EnrichHistoryToolPartsForAgent(sessionID string, history []api.Message, stash *PlanToolStash) []api.Message {
	if stash == nil || len(history) == 0 {
		return history
	}
	out := append([]api.Message(nil), history...)
	var pending []string
	for i := range out {
		msg := &out[i]
		switch msg.Role {
		case api.MessageRoleAssistant:
			pending = nil
			for _, tc := range msg.ToolCalls {
				if id := strings.TrimSpace(tc.ID); id != "" {
					pending = append(pending, id)
				}
			}
		case api.MessageRoleTool:
			callID := ""
			if len(pending) > 0 {
				callID = pending[0]
				pending = pending[1:]
			}
			if callID == "" {
				continue
			}
			block, ok := stash.Get(sessionID, callID)
			if !ok {
				continue
			}
			if msg.ToolResult != nil && strings.HasPrefix(strings.TrimSpace(msg.ToolResult.PrimaryCode()), "SPEC_") {
				continue
			}
			if msg.ToolResult == nil || msg.ToolResult.Outcome != api.ToolResultOutcomeRejected {
				msg.Content = block
				if msg.ToolResult != nil {
					msg.ToolResult.Content = block
				}
			}
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return out
}
