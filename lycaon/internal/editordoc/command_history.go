package editordoc

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/documentcore"
)

type CommandHistory struct {
	OperationID  string `json:"operation_id"`
	Epoch        int64  `json:"epoch"`
	BeforeUpdate []byte `json:"before_update"`
	Update       []byte `json:"update"`
}

// The dependency delta is remote context; only Update is the person's command.
func (s *Service) captureCommandHistory(ctx context.Context, d *Document, entry *replicaEntry, operationID string, vector []byte) error {
	if len(vector) == 0 {
		return nil
	}
	before, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "inspect", OmitText: true, Handle: entry.handle, Vector: vector})
	if err != nil {
		return err
	}
	d.CommandHistory = &CommandHistory{OperationID: operationID, Epoch: entry.head.Epoch, BeforeUpdate: before.Update}
	return nil
}

func commandHistoryJSON(history *CommandHistory) ([]byte, error) {
	if history == nil {
		return nil, nil
	}
	return json.Marshal(history)
}
