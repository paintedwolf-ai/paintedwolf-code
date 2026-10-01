package store

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/messageview"
)

func (s *SQL) GetChunkProjection(ctx context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error) {
	raw, err := s.queries.GetChunkProjection(ctx, db.GetChunkProjectionParams{SessionID: sessionID, MessageID: messageID})
	if db.IsNoRows(err) {
		return messageview.ChunkProjection{}, false, nil
	}
	if err != nil {
		return messageview.ChunkProjection{}, false, err
	}
	var projection messageview.ChunkProjection
	err = json.Unmarshal([]byte(raw), &projection)
	return projection, err == nil, err
}

func (s *SQL) PutChunkProjection(ctx context.Context, sessionID, messageID string, projection messageview.ChunkProjection) error {
	raw, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	return s.queries.PutChunkProjection(ctx, db.PutChunkProjectionParams{
		SessionID: sessionID, MessageID: messageID, ProjectionJson: string(raw),
	})
}

func (s *Memory) GetChunkProjection(ctx context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error) {
	if err := ctx.Err(); err != nil {
		return messageview.ChunkProjection{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	projection, ok := s.chunkProjections[sessionID][messageID]
	return cloneChunkProjection(projection), ok, nil
}

func (s *Memory) PutChunkProjection(ctx context.Context, sessionID, messageID string, projection messageview.ChunkProjection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.messages[sessionID] {
		if msg.ID != messageID {
			continue
		}
		if s.chunkProjections == nil {
			s.chunkProjections = make(map[string]map[string]messageview.ChunkProjection)
		}
		if s.chunkProjections[sessionID] == nil {
			s.chunkProjections[sessionID] = make(map[string]messageview.ChunkProjection)
		}
		s.chunkProjections[sessionID][messageID] = cloneChunkProjection(projection)
		break
	}
	return nil
}

func cloneChunkProjection(projection messageview.ChunkProjection) messageview.ChunkProjection {
	if projection.Meta != nil {
		meta := *projection.Meta
		projection.Meta = &meta
	}
	return projection
}
