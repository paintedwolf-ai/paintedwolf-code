package upgradefixture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/editoroutbox"
)

type EditorEvidence struct {
	DocumentID             string `json:"document_id"`
	ClientID               string `json:"client_id"`
	Epoch                  int64  `json:"epoch"`
	AcceptedText           string `json:"accepted_text"`
	LocalText              string `json:"local_text"`
	SavedText              string `json:"saved_text"`
	SaveOperationID        string `json:"save_operation_id"`
	SaveRevision           int64  `json:"save_revision"`
	PendingSaveOperationID string `json:"pending_save_operation_id"`
	PendingSaveRevision    int64  `json:"pending_save_revision"`
	AcceptedOperationID    string `json:"accepted_operation_id"`
	PendingOperationID     string `json:"pending_operation_id"`
	OutboxPath             string `json:"outbox_path"`
	OutboxSHA256           string `json:"outbox_sha256"`
}

// VerifyEditorHistory reads the production store and codecs without delivering
// pending work, so restart and relocated-restore rehearsals can repeat it.
func VerifyEditorHistory(ctx context.Context, database db.Handle, dataDir, projectID string, evidence EditorEvidence) error {
	if evidence.DocumentID == "" || evidence.SaveOperationID == "" || evidence.PendingSaveOperationID == "" || !filepath.IsLocal(evidence.OutboxPath) {
		return fmt.Errorf("editor fixture evidence is incomplete")
	}
	if err := editoroutbox.Validate(ctx, dataDir); err != nil {
		return err
	}
	store := editordoc.NewStore(database)
	document, err := store.Get(ctx, evidence.DocumentID)
	if err != nil {
		return err
	}
	if document.ProjectID != projectID || document.Draft != evidence.AcceptedText || document.BaseContent != evidence.SavedText || !document.Dirty {
		return fmt.Errorf("retained editor draft or saved base changed")
	}
	mutation, err := store.Mutation(ctx, evidence.SaveOperationID)
	if err != nil {
		return err
	}
	if mutation.Status != "complete" || mutation.DraftRevision != evidence.SaveRevision || mutation.DocumentID != evidence.DocumentID || mutation.ClientID != evidence.ClientID {
		return fmt.Errorf("retained save replay changed")
	}
	var revision int64
	var client string
	if err := database.QueryRowContext(ctx, `SELECT revision,client_id FROM editor_save_pins WHERE operation_id=? AND document_id=?`, evidence.PendingSaveOperationID, evidence.DocumentID).Scan(&revision, &client); err != nil {
		return err
	}
	if revision != evidence.PendingSaveRevision || client != evidence.ClientID {
		return fmt.Errorf("pending save reservation changed")
	}
	var contributions int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM source_text_contributions WHERE document_id=? AND operation_id=? AND person_id IS NOT NULL`, evidence.DocumentID, evidence.AcceptedOperationID).Scan(&contributions); err != nil {
		return err
	}
	if contributions == 0 {
		return fmt.Errorf("retained editor authorship is missing")
	}
	return verifyEditorCore(ctx, database, dataDir, evidence)
}

type outboxRecord struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
	State       []byte `json:"state"`
	Update      []byte `json:"update"`
}

// preservedRecords reads the frames the outbox header publishes; the evidence digest covers the record log.
func preservedRecords(dataDir string, evidence EditorEvidence) ([]outboxRecord, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, evidence.OutboxPath))
	if err != nil {
		return nil, err
	}
	if digest(raw) != evidence.OutboxSHA256 {
		return nil, fmt.Errorf("preserved editor envelope changed")
	}
	headerRaw, err := os.ReadFile(filepath.Join(dataDir, filepath.Dir(evidence.OutboxPath), "header.json"))
	if err != nil {
		return nil, err
	}
	var header struct {
		LogBytes int64 `json:"logBytes"`
		Frames   []struct {
			Offset int64 `json:"offset"`
			Length int64 `json:"length"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, err
	}
	if header.LogBytes > int64(len(raw)) {
		return nil, fmt.Errorf("preserved editor header names bytes beyond the record log")
	}
	records := make([]outboxRecord, 0, len(header.Frames))
	for _, frame := range header.Frames {
		if frame.Offset < 0 || frame.Length <= 0 || frame.Offset > header.LogBytes-frame.Length {
			return nil, fmt.Errorf("preserved editor frame outside the committed log")
		}
		var record outboxRecord
		if err := json.Unmarshal(raw[frame.Offset:frame.Offset+frame.Length], &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func verifyEditorCore(ctx context.Context, database db.Handle, dataDir string, evidence EditorEvidence) error {
	records, err := preservedRecords(dataDir, evidence)
	if err != nil {
		return err
	}
	engine, err := documentcore.New(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close(context.WithoutCancel(ctx)) }()
	var checkpoint []byte
	var revision, epoch int64
	if err := database.QueryRowContext(ctx, `SELECT checkpoint,checkpoint_revision,epoch FROM editor_replica_heads WHERE document_id=?`, evidence.DocumentID).Scan(&checkpoint, &revision, &epoch); err != nil {
		return err
	}
	if epoch != evidence.Epoch {
		return fmt.Errorf("retained editor epoch changed")
	}
	accepted, err := engine.Call(ctx, documentcore.Request{Action: "open", Handle: 1, Client: 1, Update: checkpoint})
	if err != nil {
		return err
	}
	rows, err := database.QueryContext(ctx, `SELECT update_bytes FROM editor_replica_updates WHERE document_id=? AND revision>? ORDER BY revision`, evidence.DocumentID, revision)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var update []byte
		if err := rows.Scan(&update); err != nil {
			return err
		}
		accepted, err = engine.Call(ctx, documentcore.Request{Action: "apply", Handle: 1, Update: update})
		if err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if accepted.Text != evidence.AcceptedText {
		return fmt.Errorf("host CRDT checkpoint changed")
	}
	pendingFound, checkpointFound := false, false
	for _, record := range records {
		if record.Kind == "checkpoint" {
			local, err := engine.Call(ctx, documentcore.Request{Action: "open", Handle: 2, Client: 2, Update: record.State})
			if err != nil {
				return err
			}
			if local.Text != evidence.LocalText {
				return fmt.Errorf("local CRDT checkpoint changed")
			}
			checkpointFound = true
		}
		if record.Kind == "update" && record.OperationID == evidence.PendingOperationID {
			local, err := engine.Call(ctx, documentcore.Request{Action: "apply", Handle: 1, Update: record.Update})
			if err != nil {
				return err
			}
			if local.Text != evidence.LocalText {
				return fmt.Errorf("pending update no longer joins host CRDT")
			}
			pendingFound = true
		}
	}
	if !pendingFound || !checkpointFound {
		return fmt.Errorf("editor pending work is missing")
	}
	return nil
}
