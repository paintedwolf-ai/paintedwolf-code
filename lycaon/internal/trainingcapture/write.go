package trainingcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// Finish persists one immutable artifact per student. Exclusions contain identifiers and
// field names only; no payload of an excluded turn is written.
func (t *Turn) Finish() error {
	if t == nil {
		return nil
	}
	body, source, count := t.wire.Snapshot()
	records := t.records
	if len(records) == 0 {
		for _, student := range t.students {
			record := t.record
			record.StudentProvider, record.StudentModel = student.Provider, student.Model
			records = append(records, record)
		}
	}
	var result error
	for _, record := range records {
		record.Dispatches = count
		if record.Reason == "" {
			switch {
			case count != 1:
				record.Reason = "ambiguous_dispatch"
			case len(body) == 0:
				record.Reason = "request_too_large"
			case source == nil:
				record.Reason = "dispatch_source_unrecorded"
			case !record.Settled:
				record.Reason = "unsettled_output"
			case len(record.Response) == 0 || len(record.StudentRequest) == 0:
				record.Reason = "missing_response"
			}
		}
		record.Request = body
		if record.Reason != "" {
			record.Request, record.StudentRequest, record.Response = nil, nil, nil
		}
		result = errors.Join(result, writeArtifact(t.root, record))
	}
	return result
}

func (t *Turn) Flush(ctx context.Context) {
	if err := t.Finish(); err != nil {
		slog.WarnContext(ctx, "write training capture", "error", err)
	}
}

// Fact is a host decision a collection joins to its captures by message.
type Fact struct {
	Schema    string `json:"schema"`
	Epoch     string `json:"epoch"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	DecidedAt string `json:"decided_at"`
}

// TurnCommitted records that the host committed a tool-free assistant message
// as the visible result of a turn: the host's own acceptance of a final reply.
func TurnCommitted(ctx context.Context, session, message string) {
	root, epoch, _, ok := config()
	if !ok {
		return
	}
	fact := Fact{Schema: "pw-training-fact/1", Epoch: epoch, Kind: "turn_committed", SessionID: session,
		MessageID: message, DecidedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeArtifact(root, fact); err != nil {
		slog.WarnContext(ctx, "write training fact", "error", err)
	}
}

func writeArtifact(root string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode training capture: %w", err)
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: uuid.NewString() + ".json"},
		Source:   bytes.NewReader(data), Mode: 0o600, DirMode: 0o700,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			_, statErr := target.Lstat()
			if errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
			return errors.New("training capture destination already exists or is unreadable")
		},
	})
	return err
}
