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

// StateTransitions requires an exact deliverable label and a changed state.
func StateTransitions(previous, current string) bool {
	before := map[string][]string{}
	for _, item := range ChecklistItems(previous) {
		before[item.Label] = append(before[item.Label], item.State)
	}
	for _, item := range ChecklistItems(current) {
		states := before[item.Label]
		if len(states) == 0 {
			continue
		}
		before[item.Label] = states[1:]
		if states[0] != item.State {
			return true
		}
	}
	return false
}

// Reconciles records a pending deliverable's explicit terminal transition.
func Reconciles(previous, current string) bool {
	before := map[string][]string{}
	for _, item := range ChecklistItems(previous) {
		before[item.Label] = append(before[item.Label], item.State)
	}
	for _, item := range ChecklistItems(current) {
		states := before[item.Label]
		if len(states) == 0 {
			continue
		}
		before[item.Label] = states[1:]
		if states[0] == ProgressStatePending && (item.State == ProgressStateDone || item.State == ProgressStateNA) {
			return true
		}
	}
	return false
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
