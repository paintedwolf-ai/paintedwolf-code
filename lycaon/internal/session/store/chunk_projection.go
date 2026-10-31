package store

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/messageview"
)

func (s *sqlCompaction) GetChunkProjection(ctx context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error) {
	raw, err := s.core.queries.GetChunkProjection(ctx, db.GetChunkProjectionParams{SessionID: sessionID, MessageID: messageID})
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

func (s *sqlCompaction) PutChunkProjection(ctx context.Context, sessionID, messageID string, projection messageview.ChunkProjection) error {
	raw, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	return s.core.queries.PutChunkProjection(ctx, db.PutChunkProjectionParams{
		SessionID: sessionID, MessageID: messageID, ProjectionJson: string(raw),
	})
}

func (s *memoryCompaction) GetChunkProjection(ctx context.Context, sessionID, messageID string) (messageview.ChunkProjection, bool, error) {
	if err := ctx.Err(); err != nil {
		return messageview.ChunkProjection{}, false, err
	}
	s.core.mu.RLock()
	defer s.core.mu.RUnlock()
	projection, ok := s.core.chunkProjections[sessionID][messageID]
	return cloneChunkProjection(projection), ok, nil
}

func (s *memoryCompaction) PutChunkProjection(ctx context.Context, sessionID, messageID string, projection messageview.ChunkProjection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	for _, msg := range core.messages[sessionID] {
		if msg.ID != messageID {
			continue
		}
		if core.chunkProjections == nil {
			core.chunkProjections = make(map[string]map[string]messageview.ChunkProjection)
		}
		if core.chunkProjections[sessionID] == nil {
			core.chunkProjections[sessionID] = make(map[string]messageview.ChunkProjection)
		}
		core.chunkProjections[sessionID][messageID] = cloneChunkProjection(projection)
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
