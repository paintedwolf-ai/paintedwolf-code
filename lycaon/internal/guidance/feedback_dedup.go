package guidance

import (
	"os"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/debugpaths"
)

// FeedbackDeduper suppresses repeated full checklist appendices per session.
// It is ephemeral host state (in-memory only).
type FeedbackDeduper struct {
	mu sync.Mutex
	m  map[string]*dedupState
}

type dedupState struct {
	blockCount int
	lastHash   string
}

func NewFeedbackDeduper() *FeedbackDeduper {
	return &FeedbackDeduper{m: make(map[string]*dedupState)}
}

// ShouldAppendDetails appends the full checklist on a session's first reject and
// whenever its hash changes; LYCAON_TOOL_FEEDBACK_VERBOSE=1 or LYCAON_DEBUG_ALL
// appends it every time.
func (d *FeedbackDeduper) ShouldAppendDetails(sessionID, checklistHash string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return true
	}
	if verboseFeedbackEnv() {
		return true
	}
	if d == nil {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.m[sessionID]
	if st == nil {
		st = &dedupState{}
		d.m[sessionID] = st
	}
	st.blockCount++
	if st.blockCount == 1 {
		st.lastHash = checklistHash
		return true
	}
	if strings.TrimSpace(checklistHash) == "" {
		return false
	}
	if checklistHash != st.lastHash {
		st.lastHash = checklistHash
		return true
	}
	return false
}

func (d *FeedbackDeduper) ClearSession(sessionID string) {
	if d == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.m, sessionID)
}

// verboseFeedbackEnv reports whether the full checklist should always append:
// its own switch, or the main debug switch.
func verboseFeedbackEnv() bool {
	return configdir.EnvTruthy(os.Getenv("LYCAON_TOOL_FEEDBACK_VERBOSE")) || debugpaths.FullDebugEnabled()
}
