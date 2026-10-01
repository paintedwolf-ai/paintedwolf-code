package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcerewind"
)

const (
	rewindJournalVersion = 2
	rewindDirName        = "rewinds"
	rewindJournalName    = "journal.json"
)

type rewindJournalPhase string

const (
	PhasePrepared     rewindJournalPhase = "prepared"
	PhaseApplying     rewindJournalPhase = "applying"
	PhaseFilesApplied rewindJournalPhase = "files_applied"
	PhaseRolledBack   rewindJournalPhase = "rolled_back"
)

type Journal struct {
	Version         int                     `json:"version"`
	ID              string                  `json:"id"`
	SessionID       string                  `json:"checkpoint_session_id"`
	TranscriptID    string                  `json:"transcript_session_id"`
	AnchorMessageID string                  `json:"anchor_message_id"`
	ProjectDir      string                  `json:"project_dir"`
	Phase           rewindJournalPhase      `json:"phase"`
	Source          *sourcerewind.Operation `json:"source"`
	path            string
	sourceApply     func() error
	sourceRollback  func() error
}

func (s *Store) rewindsRoot() string { return filepath.Join(s.root, rewindDirName) }

func LoadJournal(path string) (*Journal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var journal Journal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("rewind journal has trailing data")
	}
	if journal.Version != rewindJournalVersion || strings.TrimSpace(journal.ID) == "" || journal.Source == nil {
		return nil, fmt.Errorf("unsupported rewind journal")
	}
	journal.path = filepath.Clean(path)
	return &journal, nil
}

func (j *Journal) Write() error {
	if j == nil || strings.TrimSpace(j.path) == "" {
		return fmt.Errorf("rewind journal path is empty")
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(j.path),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

func (j *Journal) Apply() ([]string, error) {
	if j.Source == nil || j.sourceApply == nil {
		return nil, fmt.Errorf("source rewind journal unavailable")
	}
	err := j.sourceApply()
	return j.Source.Paths(), err
}
func (j *Journal) Rollback() error {
	if j.Source == nil || j.sourceRollback == nil {
		return fmt.Errorf("source rewind journal unavailable")
	}
	return j.sourceRollback()
}
func (j *Journal) VerifyTargets() error {
	if j.Source.Phase == "files_applied" {
		return nil
	}
	return j.Source.Verify()
}

func (j *Journal) Cleanup(checkpoint *Store) error {
	if j == nil {
		return nil
	}
	if checkpoint == nil {
		return fmt.Errorf("rewind cleanup requires a checkpoint store")
	}
	id, ok := safeIDSegment(j.ID)
	if !ok {
		return fmt.Errorf("invalid rewind cleanup identity")
	}
	dir := filepath.Dir(filepath.Clean(j.path))
	want := filepath.Join(checkpoint.rewindsRoot(), id)
	if dir != want {
		return fmt.Errorf("rewind cleanup path escaped operation directory")
	}
	return bloblifecycle.RemoveTree(checkpoint.stateRoot, dir)
}

func (j *Journal) Path() string { return j.path }

func NewJournal(cp *Store, man *Manifest, id, sessionID string, source *sourcerewind.Operation) (*Journal, error) {
	if _, ok := safeIDSegment(id); !ok {
		return nil, fmt.Errorf("invalid rewind operation id")
	}
	j := &Journal{Version: rewindJournalVersion, ID: id, SessionID: man.SessionID, TranscriptID: sessionID,
		AnchorMessageID: man.AnchorMessageID, ProjectDir: cp.projectDir, Phase: PhasePrepared, Source: source,
		path: filepath.Join(cp.rewindsRoot(), id, rewindJournalName)}
	if err := os.MkdirAll(filepath.Dir(j.path), checkpointDirPerm); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *Journal) Bind(ctx context.Context, service *sourcerewind.Service, p *project.Project) error {
	if err := j.Source.ValidateWorkspace(p); err != nil {
		return err
	}
	save := func() error {
		switch j.Source.Phase {
		case "files_applied":
			j.Phase = PhaseFilesApplied
		case "rolled_back":
			j.Phase = PhaseRolledBack
		default:
			j.Phase = PhaseApplying
		}
		return j.Write()
	}
	j.sourceApply = func() error { return service.Apply(ctx, p, j.Source, save) }
	j.sourceRollback = func() error { return service.RollbackProject(ctx, p, j.Source, save) }
	return nil
}
