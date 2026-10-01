package visual

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// AttachToolResult stores a capture and stamps its ID into the tool result.
func AttachToolResult(
	ctx context.Context,
	store Store,
	rootSessionID, producerSessionID, toolCallID string,
	tr *api.ToolResult,
	cap *tools.VisualCapture,
	content string,
) (stamped string, err error) {
	if store == nil || tr == nil || cap == nil {
		return content, nil
	}
	if id := strings.TrimSpace(cap.ArtifactID); id != "" {
		res := store.Resolve(ctx, rootSessionID, id)
		if !res.IsPresent() {
			return content, fmt.Errorf("visual artifact %s is not present", id)
		}
		meta := res.Meta()
		meta.Perceive = cap.Perceive
		tr.Visual = &meta
		return StampArtifactID(content, meta.ID), nil
	}
	if len(cap.Bytes) == 0 {
		return content, fmt.Errorf("visual capture bytes are empty")
	}
	if cap.Source != api.VisualArtifactSourceUser && cap.Source != api.VisualArtifactSourceWorkspace && !cap.Projected {
		return content, fmt.Errorf("visual capture is not a safe projection")
	}
	wire, err := store.Put(ctx, rootSessionID, Entry{
		Meta: api.VisualArtifact{
			Mime:       cap.Mime,
			Source:     cap.Source,
			Caption:    cap.Caption,
			Perceive:   cap.Perceive,
			ToolCallID: toolCallID,
			Width:      cap.Width,
			Height:     cap.Height,
		},
		ProducerSessionID: producerSessionID,
		Bytes:             cap.Bytes,
	})
	if err != nil {
		return content, err
	}
	tr.Visual = &wire
	return StampArtifactID(content, wire.ID), nil
}
