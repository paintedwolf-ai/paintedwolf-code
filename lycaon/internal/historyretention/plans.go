package historyretention

import (
	"context"
	"encoding/json"
	"os"

	"github.com/google/uuid"
)

// Preview candidates spill to an unlinked file; execution reads bounded batches.
func newPlanSpool() (*os.File, error) {
	f, err := os.CreateTemp("", "history-preview-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Service) closePlans() {
	for token, p := range s.plans {
		_ = p.spool.Close()
		delete(s.plans, token)
	}
}

func (s *Service) continuePlan(ctx context.Context, p plan) (string, error) {
	p.items = nil
	start, err := p.spool.Seek(0, 1)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(p.spool)
	for len(p.items) < previewBatch && p.remaining > 0 {
		var item candidate
		if err := decoder.Decode(&item); err != nil {
			return "", err
		}
		p.items = append(p.items, item)
		p.remaining--
	}
	// Resume at the consumed offset; the decoder may have read ahead.
	if _, err := p.spool.Seek(start+decoder.InputOffset(), 0); err != nil {
		return "", err
	}
	p.generation, err = s.generation(ctx)
	if err != nil {
		return "", err
	}
	token := uuid.NewString()
	s.plans[token] = p
	return token, nil
}
