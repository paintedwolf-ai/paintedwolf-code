package checkpoint

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/session/store"
)

// JournalRelPath binds a journal to installation-owned checkpoint storage.
func JournalRelPath(projectDir, operationID string) (string, error) {
	id, ok := safeIDSegment(operationID)
	if !ok || id != operationID || strings.TrimSpace(projectDir) == "" {
		return "", fmt.Errorf("invalid rewind journal identity")
	}
	root := enginepaths.ProjectCheckpointDir(enginepaths.SessionCheckpointsDirName, filepath.Clean(projectDir))
	return filepath.Join(root, rewindDirName, id, rewindJournalName), nil
}

func LoadOperationJournal(stateRoot string, op store.RewindOperation) (*Journal, error) {
	rel, err := JournalRelPath(op.ProjectDir, op.ID)
	if err != nil {
		return nil, err
	}
	journal, err := LoadJournal(filepath.Join(stateRoot, rel))
	if err != nil {
		return nil, err
	}
	if err := journal.ValidateOperation(op); err != nil {
		return nil, err
	}
	return journal, nil
}

func (j *Journal) ValidateOperation(op store.RewindOperation) error {
	if j.ID != op.ID || j.TranscriptID != op.SessionID || j.AnchorMessageID != op.AnchorMessageID || filepath.Clean(j.ProjectDir) != filepath.Clean(op.ProjectDir) {
		return fmt.Errorf("rewind operation identity diverged")
	}
	return nil
}
