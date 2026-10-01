package session

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/workercontext"
)

// maxPeerRejectionLineRunes bounds one peer rejection line in the feed.
const maxPeerRejectionLineRunes = 160

const peerRejectionFeedCap = 128

var peerShareRejectCodes = map[string]struct{}{
	"GREP_PATH_NOT_FOUND": {},
	"FIND_PATH_NOT_FOUND": {},
	"READ_PATH_NOT_FOUND": {},
	"SURVEY_PATH_ESCAPE":  {},
	"DOOM_LOOP_REPEAT":    {},
}

// PeerRejectionFeed records tool rejects that sibling workers should not repeat.
type PeerRejectionFeed struct {
	mu sync.RWMutex
	// entries bounds notes by root session.
	entries scopedstore.LRU[[]inject.SiblingNote]
}

func NewPeerRejectionFeed() *PeerRejectionFeed {
	return &PeerRejectionFeed{}
}

// RecordPeerToolReject stores a shareable structured rejection.
func (f *PeerRejectionFeed) RecordPeerToolReject(rootSessionID, agent, code, content string, facts guidance.ToolResultFacts) {
	if f == nil {
		return
	}
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" {
		return
	}
	code = strings.TrimSpace(code)
	if _, ok := peerShareRejectCodes[code]; !ok {
		return
	}
	summary := peerRejectSummary(code, facts, content)
	ref := peerRejectRef(code, facts)
	if summary == "" {
		return
	}
	note := inject.SiblingNote{
		Agent:   strings.TrimSpace(agent),
		Summary: summary,
		Ref:     ref,
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	list, _ := f.entries.Load(rootSessionID)
	if len(list) > 0 {
		last := list[len(list)-1]
		if last.Summary == note.Summary && last.Ref == note.Ref {
			return
		}
	}
	list = append(list, note)
	if len(list) > peerRejectionFeedCap {
		list = list[len(list)-peerRejectionFeedCap:]
	}
	f.entries.Store(rootSessionID, list)
}

// RecentForRoot returns the newest peer rejection notes for a coordinator root session.
func (f *PeerRejectionFeed) RecentForRoot(rootSessionID string, max int) []inject.SiblingNote {
	if f == nil || max <= 0 {
		return nil
	}
	rootSessionID = strings.TrimSpace(rootSessionID)
	f.mu.RLock()
	defer f.mu.RUnlock()
	list, _ := f.entries.Load(rootSessionID)
	if len(list) == 0 {
		return nil
	}
	start := len(list) - max
	if start < 0 {
		start = 0
	}
	out := make([]inject.SiblingNote, 0, len(list)-start)
	for _, note := range list[start:] {
		out = append(out, inject.SiblingNote{
			Agent:   note.Agent,
			Summary: note.Summary,
			Ref:     note.Ref,
		})
	}
	return out
}

func pathFromFacts(facts guidance.ToolResultFacts) string {
	for _, fb := range facts.Feedback {
		if p, ok := fb.Details["path"].(string); ok && strings.TrimSpace(p) != "" {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

func peerRejectSummary(code string, facts guidance.ToolResultFacts, content string) string {
	code = strings.TrimSpace(code)
	switch code {
	case "GREP_PATH_NOT_FOUND", "FIND_PATH_NOT_FOUND", "READ_PATH_NOT_FOUND":
		if path := pathFromFacts(facts); path != "" {
			return code + ": " + path + " does not exist in project scope"
		}
	case "DOOM_LOOP_REPEAT":
		return "Identical tool call blocked — do not replay the same args"
	}
	line := strings.TrimSpace(content)
	if idx := strings.Index(line, "\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	line = strings.TrimPrefix(line, hostmarker.Rejected)
	line = strings.TrimSpace(line)
	line = runeclamp.Fit(line, maxPeerRejectionLineRunes)
	return line
}

func peerRejectRef(code string, facts guidance.ToolResultFacts) string {
	if path := pathFromFacts(facts); path != "" {
		return path
	}
	return strings.TrimSpace(code)
}

func (m *Manager) recordPeerToolReject(ctx context.Context, childSessionID, code, content string, facts guidance.ToolResultFacts) {
	if m == nil || m.peerRejections == nil || m.store == nil {
		return
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return
	}
	sess, err := m.store.Get(ctx, childSessionID)
	if err != nil || sess == nil || !sess.IsWorkerChild() {
		return
	}
	root := RootSessionID(ctx, m.store, childSessionID)
	agent := strings.TrimSpace(sess.AgentType)
	if m.workerQueue != nil {
		if task, ok := m.workerQueue.Get(workercontext.Job(ctx)); ok && task != nil {
			if id := strings.TrimSpace(task.ID); id != "" {
				agent = id
			}
		}
	}
	m.peerRejections.RecordPeerToolReject(root, agent, code, content, facts)
}
