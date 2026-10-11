package upgradefixture

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	sourceHistoryPath = "upgrade-fixture-history.txt"
	currentSourceBody = "The current file differs from retained history.\n"
)

type SourceEvidence struct {
	CheckpointAnchorID string `json:"checkpoint_anchor_id"`
	CheckpointRootKey  string `json:"checkpoint_root_key"`
	SourceVersionID    string `json:"source_version_id"`
	CurrentVersionID   string `json:"current_version_id"`
	SourceFileID       string `json:"source_file_id"`
	BodySHA256         string `json:"body_sha256"`
}

func sourceHistoryBody() []byte {
	return []byte(strings.Repeat("Preserved source history and rewind preimage.\n", 256))
}

func SeedSourceHistory(ctx context.Context, database db.Handle, dataDir, projectDir, projectID, sessionID string) (SourceEvidence, error) {
	if _, err := os.Lstat(filepath.Join(projectDir, sourceHistoryPath)); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return SourceEvidence{}, fmt.Errorf("source fixture path already exists")
		}
		return SourceEvidence{}, err
	}
	var rootID string
	if err := database.QueryRowContext(ctx, `SELECT id FROM project_roots WHERE project_id=? AND path=?`, projectID, projectDir).Scan(&rootID); err != nil {
		return SourceEvidence{}, fmt.Errorf("resolve source fixture root: %w", err)
	}
	body := sourceHistoryBody()
	if err := writeSourceFixture(projectDir, body); err != nil {
		return SourceEvidence{}, err
	}
	ledger := sourceledger.New(database, filepath.Join(dataDir, enginepaths.SourceContentDirName))
	defer func() { _ = ledger.Snapshots.Close() }()
	if err := ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: sourceHistoryPath},
		ProjectID:      projectID,
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		SessionID: sessionID, OperationID: uuid.NewString(), After: body}); err != nil {
		return SourceEvidence{}, fmt.Errorf("record fixture source body: %w", err)
	}
	fileID, versionID, err := ledger.History.ResolveFile(ctx, projectID, sourcebranch.Trunk, rootID, sourceHistoryPath)
	if err != nil {
		return SourceEvidence{}, err
	}
	evidence := SourceEvidence{
		CheckpointAnchorID: uuid.NewString(), CheckpointRootKey: enginepaths.ProjectKey(projectDir),
		SourceFileID: fileID, SourceVersionID: versionID, BodySHA256: sourceblob.ContentSHA(body),
	}
	checkpoints := sessioncheckpoint.New(dataDir, projectDir, checkpointRepository(database))
	if _, err := checkpoints.Open(ctx, sessionID, evidence.CheckpointAnchorID); err != nil {
		return SourceEvidence{}, fmt.Errorf("open fixture checkpoint: %w", err)
	}
	if err := checkpoints.CapturePreImage(ctx, sessionID, evidence.CheckpointAnchorID, sourceHistoryPath); err != nil {
		return SourceEvidence{}, fmt.Errorf("capture fixture checkpoint: %w", err)
	}
	changed := []byte(currentSourceBody)
	if err := writeSourceFixture(projectDir, changed); err != nil {
		return SourceEvidence{}, err
	}
	if err := ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: sourceHistoryPath},
		ProjectID:      projectID,
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
		SessionID: sessionID, OperationID: uuid.NewString(), Before: body, After: changed}); err != nil {
		return SourceEvidence{}, fmt.Errorf("record changed fixture source body: %w", err)
	}
	_, evidence.CurrentVersionID, err = ledger.History.ResolveFile(ctx, projectID, sourcebranch.Trunk, rootID, sourceHistoryPath)
	return evidence, err
}

func writeSourceFixture(projectDir string, body []byte) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: projectDir, Rel: sourceHistoryPath},
		Source:   bytes.NewReader(body), Mode: 0o640, DirMode: 0o700,
	})
	return err
}

func VerifySourceHistory(ctx context.Context, database db.Handle, dataDir, projectID, sessionID string, evidence SourceEvidence) error {
	if evidence.SourceVersionID == "" || evidence.CurrentVersionID == evidence.SourceVersionID {
		return fmt.Errorf("source fixture has no independent historical version")
	}
	contentDir := filepath.Join(dataDir, enginepaths.SourceContentDirName)
	ledger := sourceledger.New(database, contentDir)
	defer func() { _ = ledger.Snapshots.Close() }()
	version, err := ledger.History.ReadRestorableVersion(ctx, projectID, evidence.SourceVersionID)
	if err != nil {
		return fmt.Errorf("read retained source version: %w", err)
	}
	if version.FileID != evidence.SourceFileID || version.SHA256 != evidence.BodySHA256 || !bytes.Equal(version.Content, sourceHistoryBody()) {
		return fmt.Errorf("retained source version changed identity or bytes")
	}
	current, err := ledger.History.ReadRestorableVersion(ctx, projectID, evidence.CurrentVersionID)
	if err != nil {
		return fmt.Errorf("read current source version: %w", err)
	}
	if current.FileID != version.FileID || string(current.Content) != currentSourceBody {
		return fmt.Errorf("source fixture lost its historical boundary")
	}
	raw, err := checkpointRepository(database).ReadCheckpoint(ctx, evidence.CheckpointRootKey, sessionID, evidence.CheckpointAnchorID)
	if err != nil {
		return fmt.Errorf("read retained checkpoint: %w", err)
	}
	var manifest sessioncheckpoint.Manifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return fmt.Errorf("decode retained checkpoint: %w", err)
	}
	entry, ok := manifest.Paths[sourceHistoryPath]
	if !ok || entry.Op != sessioncheckpoint.OpSnapshot || entry.SHA256 != evidence.BodySHA256 || entry.Mode != 0o640 || entry.Size != int64(len(sourceHistoryBody())) || manifest.Truncated || manifest.SessionID != sessionID || manifest.AnchorMessageID != evidence.CheckpointAnchorID {
		return fmt.Errorf("checkpoint preimage changed identity or permissions")
	}
	body, err := sourceblob.New(contentDir).GetSHA(entry.SHA256)
	if err != nil {
		return fmt.Errorf("read compressed checkpoint preimage: %w", err)
	}
	if !bytes.Equal(body, sourceHistoryBody()) {
		return fmt.Errorf("checkpoint preimage changed bytes")
	}
	return verifyReconstructedPreimage(body, entry.Mode)
}

type fixtureReadHandle struct{ db.Handle }

func (h fixtureReadHandle) BeginReadTx(ctx context.Context) (*sql.Tx, error) {
	return h.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

func checkpointRepository(database db.Handle) *store.SQL {
	if reader, ok := database.(db.ReadHandle); ok {
		return store.NewSQL(reader)
	}
	return store.NewSQL(fixtureReadHandle{Handle: database})
}

func verifyReconstructedPreimage(body []byte, mode uint32) error {
	scratch, err := os.MkdirTemp("", "upgrade-history-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := writeSourceFixture(scratch, body); err != nil {
		return err
	}
	path := filepath.Join(scratch, sourceHistoryPath)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if uint32(info.Mode().Perm()) != mode || !bytes.Equal(restored, body) {
		return fmt.Errorf("checkpoint reconstruction changed bytes or permissions")
	}
	return nil
}
