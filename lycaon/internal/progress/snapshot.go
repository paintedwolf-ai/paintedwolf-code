package progress

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Snapshot binds the complete document to its workflow run.
type Snapshot struct {
	Content        string          `json:"content"`
	Revision       string          `json:"revision"`
	RunID          string          `json:"run_id,omitempty"`
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
	version        string
}

func NewSnapshot(runID, content string) Snapshot {
	content = strings.TrimSpace(content)
	sum := sha256.Sum256([]byte(runID + "\x00" + content))
	return Snapshot{Content: content, Revision: hex.EncodeToString(sum[:]), RunID: runID}
}

// Transitions distinguish deliverable state changes from document edits.
type Transitions struct{ Changed, Closed bool }

// ClassifyTransitions compares exact-label state multisets, independent of row order.
func ClassifyTransitions(previous, current string) Transitions {
	before, after := checklistStates(previous), checklistStates(current)
	result := Transitions{}
	for label, oldStates := range before {
		newStates := after[label]
		oldUnmatched, newUnmatched := 0, 0
		for state, count := range oldStates {
			oldUnmatched += max(0, count-newStates[state])
		}
		for state, count := range newStates {
			newUnmatched += max(0, count-oldStates[state])
		}
		result.Changed = result.Changed || oldUnmatched > 0 && newUnmatched > 0
		closed := max(0, newStates[ProgressStateDone]-oldStates[ProgressStateDone]) + max(0, newStates[ProgressStateNA]-oldStates[ProgressStateNA])
		result.Closed = result.Closed || oldStates[ProgressStatePending] > newStates[ProgressStatePending] && closed > 0
	}
	return result
}

func checklistStates(content string) map[string]map[string]int {
	states := map[string]map[string]int{}
	for _, item := range ChecklistItems(content) {
		if states[item.Label] == nil {
			states[item.Label] = map[string]int{}
		}
		states[item.Label][item.State]++
	}
	return states
}

// Reconciliation identifies the worker finishes awaiting acknowledgement.
type Reconciliation struct {
	Token string   `json:"token"`
	Jobs  []string `json:"jobs"`
}

func snapshotVersion(runID, content, version string) Snapshot {
	snapshot := NewSnapshot(runID, content)
	sum := sha256.Sum256([]byte(snapshot.Revision + "\x00" + version))
	snapshot.Revision = hex.EncodeToString(sum[:])
	snapshot.version = version
	return snapshot
}
