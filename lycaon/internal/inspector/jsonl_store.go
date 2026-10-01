package inspector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fssync"
)

// JSONLStore appends and reads evidence JSONL under the project host data dir.
type JSONLStore struct {
	Root string
	mu   sync.Mutex
}

// NewJSONLStore creates an evidence store with the given root dir name.
func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = DefaultEvidenceDir
	}
	return &JSONLStore{Root: root}
}

type jsonlRecord struct {
	RecordID       string               `json:"record_id,omitempty"`
	Type           evidence.GateType    `json:"type"`
	Slot           string               `json:"slot"`
	RunID          string               `json:"run_id"`
	Verdict        evidence.GateVerdict `json:"verdict"`
	Summary        string               `json:"summary,omitempty"`
	Artifacts      map[string]any       `json:"artifacts,omitempty"`
	InspectorAgent string               `json:"inspector_agent,omitempty"`
	Model          string               `json:"model,omitempty"`
	Attempt        int                  `json:"attempt,omitempty"`
	HeadSHA        string               `json:"head_sha,omitempty"`
	At             time.Time            `json:"at"`
}

func toJSONLRecord(r evidence.Record) jsonlRecord {
	at := r.GateAt()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return jsonlRecord{
		RecordID:       r.RecordID,
		Type:           r.TypedGateType(),
		Slot:           r.Slot,
		RunID:          r.RunID,
		Verdict:        r.TypedGateVerdict(),
		Summary:        r.Summary,
		Artifacts:      r.Artifacts,
		InspectorAgent: r.InspectorAgent,
		Model:          r.GateModel,
		Attempt:        r.Attempt,
		HeadSHA:        r.HeadSHA,
		At:             at,
	}
}

func fromJSONLRecord(r jsonlRecord) evidence.Record {
	record := evidence.GateRecord(
		r.Type, r.Slot, r.RunID, r.Verdict, r.Summary, r.Artifacts,
		r.InspectorAgent, r.Model, r.HeadSHA, r.Attempt, r.At,
	)
	record.RecordID = r.RecordID
	return record
}

// Append writes one evidence line.
func (s *JSONLStore) Append(ctx context.Context, projectDir string, record evidence.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(projectDir, EvidencePath(s.Root, record.RunID, record.Slot, record.TypedGateType()))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(toJSONLRecord(record))
	if err != nil {
		return err
	}
	if record.RecordID != "" {
		if existing, readErr := os.ReadFile(path); readErr == nil {
			for _, candidate := range bytes.Split(existing, []byte{'\n'}) {
				var row jsonlRecord
				if json.Unmarshal(candidate, &row) == nil && row.RecordID == record.RecordID {
					if bytes.Equal(bytes.TrimSpace(candidate), line) {
						return nil
					}
					return fmt.Errorf("evidence record id %s was already used for different content", record.RecordID)
				}
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return fssync.File(f)
}

// ReadAll returns all evidence records for a gate type.
func (s *JSONLStore) ReadAll(ctx context.Context, projectDir, runID, slot string, gateType evidence.GateType) ([]evidence.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(projectDir, EvidencePath(s.Root, runID, slot, gateType))
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []evidence.Record
	decoder := json.NewDecoder(f)
	for {
		var raw jsonlRecord
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode evidence record: %w", err)
		}
		out = append(out, fromJSONLRecord(raw))
	}

}

// Latest returns the last record or nil.
func Latest(records []evidence.Record) *evidence.Record {
	if len(records) == 0 {
		return nil
	}
	last := records[len(records)-1]
	return &last
}

var _ EvidenceStore = (*JSONLStore)(nil)

func formatMissing(runID, slot string, gateType evidence.GateType) string {
	return fmt.Sprintf("%s/%s/%s", runID, slot, gateType)
}
