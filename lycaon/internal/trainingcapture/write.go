package trainingcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/observability"
)

// Finish persists one immutable artifact. Exclusions contain identifiers only;
// scrubbed requests must never masquerade as the actual dispatched request.
func (t *Turn) Finish() error {
	if t == nil {
		return nil
	}
	body, count := t.wire.Snapshot()
	t.record.Dispatches = count
	if t.record.Reason == "" {
		switch {
		case count != 1:
			t.record.Reason = "ambiguous_dispatch"
		case len(body) == 0:
			t.record.Reason = "request_too_large"
		case !t.record.Settled:
			t.record.Reason = "unsettled_output"
		case len(t.record.Response) == 0:
			t.record.Reason = "missing_response"
		}
	}
	t.record.Request = body
	if t.record.Reason == "" && (!unchangedAfterScreen(body) || !unchangedAfterScreen(t.record.Response)) {
		t.record.Reason = "secret_screen_changed_payload"
	}
	if t.record.Reason != "" {
		t.record.Request, t.record.Response = nil, nil
	}
	data, err := json.Marshal(t.record)
	if err != nil {
		return fmt.Errorf("encode training capture: %w", err)
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: t.root, Rel: uuid.NewString() + ".json"},
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

func unchangedAfterScreen(raw []byte) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return reflect.DeepEqual(value, observability.RedactCaptureValue(value))
}

func (t *Turn) Flush(ctx context.Context) {
	if err := t.Finish(); err != nil {
		slog.WarnContext(ctx, "write training capture", "error", err)
	}
}
