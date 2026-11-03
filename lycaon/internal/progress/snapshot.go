package progress

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Document pairs checklist markdown with stable item identities aligned by row index.
type Document struct {
	Content string
	ItemIDs []string
}

// Item pairs a checklist row's stable identity with its parsed label and state.
type Item struct {
	ID    string
	Label string
	State string
}

// Snapshot binds the complete document to its workflow run.
type Snapshot struct {
	Content         string          `json:"content"`
	ItemIDs         []string        `json:"item_ids,omitempty"`
	Revision        string          `json:"revision"`
	RunID           string          `json:"run_id,omitempty"`
	Reconciliation  *Reconciliation `json:"reconciliation,omitempty"`
	version         string
	documentVersion uint64
}

// Items zips parsed checklist rows with their stable item identities.
func (s Snapshot) Items() []Item {
	parsed := ChecklistItems(s.Content)
	if len(parsed) == 0 {
		return nil
	}
	out := make([]Item, len(parsed))
	for i, row := range parsed {
		var id string
		if i < len(s.ItemIDs) {
			id = s.ItemIDs[i]
		}
		out[i] = Item{
			ID:    id,
			Label: row.Label,
			State: row.State,
		}
	}
	return out
}

// DocumentVersion returns the committed monotonic document version.
func (s Snapshot) DocumentVersion() uint64 { return s.documentVersion }

// WithDocumentVersion returns a copy with the committed document version attached.
func (s Snapshot) WithDocumentVersion(version uint64) Snapshot {
	s.documentVersion = version
	return s
}

// Equal reports whether two snapshots carry identical content, item IDs, revision, run ID, and version.
func (s Snapshot) Equal(other Snapshot) bool {
	return s.Content == other.Content &&
		slices.Equal(s.ItemIDs, other.ItemIDs) &&
		s.Revision == other.Revision &&
		s.RunID == other.RunID &&
		s.version == other.version &&
		s.documentVersion == other.documentVersion &&
		reconciliationEqual(s.Reconciliation, other.Reconciliation)
}

func reconciliationEqual(a, b *Reconciliation) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Token == b.Token && slices.Equal(a.Jobs, b.Jobs)
}

func NewSnapshot(runID, content string, itemIDs ...string) Snapshot {
	content = strings.TrimSpace(content)
	ids := compactItemIDs(len(ChecklistItems(content)), itemIDs)
	sum := sha256.Sum256([]byte(runID + "\x00" + strings.Join(ids, "\x00") + "\x00" + content))
	return Snapshot{Content: content, ItemIDs: ids, Revision: hex.EncodeToString(sum[:]), RunID: runID}
}

// Transitions distinguish deliverable state changes from document edits.
type Transitions struct{ Changed, Closed bool }

// ClassifyTransitions compares items sharing the same stable identity across snapshots.
// Reordering or relabeling existing IDs does not claim deliverable advancement.
func ClassifyTransitions(before, after Snapshot) Transitions {
	prevItems := before.Items()
	nextItems := after.Items()
	if len(prevItems) == 0 || len(nextItems) == 0 {
		return Transitions{}
	}
	prevByID := make(map[string]Item, len(prevItems))
	for _, item := range prevItems {
		if item.ID != "" {
			prevByID[item.ID] = item
		}
	}
	result := Transitions{}
	for _, next := range nextItems {
		if next.ID == "" {
			continue
		}
		prev, ok := prevByID[next.ID]
		if !ok || prev.State == next.State {
			continue
		}
		result.Changed = true
		if prev.State == ProgressStatePending && (next.State == ProgressStateDone || next.State == ProgressStateNA) {
			result.Closed = true
		}
	}
	return result
}

// Reconciliation identifies the worker finishes awaiting acknowledgement.
type Reconciliation struct {
	Token string   `json:"token"`
	Jobs  []string `json:"jobs"`
}

func snapshotVersion(runID, content string, itemIDs []string, version string, docVersion uint64) Snapshot {
	snapshot := NewSnapshot(runID, content, itemIDs...)
	sum := sha256.Sum256([]byte(snapshot.Revision + "\x00" + version + "\x00" + strconv.FormatUint(docVersion, 10)))
	snapshot.Revision = hex.EncodeToString(sum[:])
	snapshot.version = version
	snapshot.documentVersion = docVersion
	return snapshot
}

func compactItemIDs(rowCount int, itemIDs []string) []string {
	if rowCount == 0 || len(itemIDs) == 0 {
		return nil
	}
	n := min(rowCount, len(itemIDs))
	return slices.Clone(itemIDs[:n])
}

func mintItemIDs(content string) []string {
	n := len(ChecklistItems(content))
	if n == 0 {
		return nil
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	return ids
}

func resolveDocumentItemIDs(current Snapshot, next Document) []string {
	rowCount := len(ChecklistItems(next.Content))
	if rowCount == 0 {
		return nil
	}
	if len(next.ItemIDs) == rowCount {
		ids := make([]string, rowCount)
		for i, id := range next.ItemIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				id = uuid.NewString()
			}
			ids[i] = id
		}
		return ids
	}
	if next.Content == current.Content && len(current.ItemIDs) == rowCount {
		return slices.Clone(current.ItemIDs)
	}
	return mintItemIDs(next.Content)
}

// Reconciler tracks pending worker finishes independently of the document.
type Reconciler interface {
	Reconciliation(string) *Reconciliation
	Acknowledge(string, string) bool
}
