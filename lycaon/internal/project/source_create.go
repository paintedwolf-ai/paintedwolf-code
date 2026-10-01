package project

import (
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/evidence"
)

// ErrSourceExists reports an occupied target.
var ErrSourceExists = errors.New("source already exists")

// Default source entry modes.
const (
	sourceCreateFileMode = 0o644
	sourceCreateDirMode  = 0o755
)

// SourceEntryKind is what a create makes at its path.
type SourceEntryKind string

const (
	SourceEntryFile   SourceEntryKind = "file"
	SourceEntryFolder SourceEntryKind = "folder"
)

// ErrSourceKindInvalid is a create for neither a file nor a folder.
var ErrSourceKindInvalid = errors.New("source entry kind invalid")

// SourceEntryCreateRequest is one in-app creation of a new file or folder.
type SourceEntryCreateRequest struct {
	// Path is the root-relative target.
	Path string
	// Kind selects a file or a folder.
	Kind SourceEntryKind
	// RootID selects one attached root.
	RootID string
	// SessionID affiliates the create with a chat.
	SessionID string
	// Turn is the chat's current turn.
	Turn int
}

// resolveFreeSourcePath resolves an unoccupied jailed target.
func resolveFreeSourcePath(p *Project, rootID, pathQuery string) (abs, rel string, err error) {
	root, err := selectSingleSourceRoot(p, rootID)
	if err != nil {
		return "", "", err
	}
	abs, rel, ok := evidence.ResolveCitationAbs(root.Path, pathQuery)
	if !ok {
		return "", "", ErrSourcePathDenied
	}
	// Lstat treats dangling symlinks as occupied targets.
	if _, err := os.Lstat(abs); err == nil {
		return "", "", ErrSourceExists
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("stat new source: %w", err)
	}
	return abs, rel, nil
}
