package inject

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// RenderBoardOrientationInject renders the Binding-resolved board-orientation inject.
func RenderBoardOrientationInject(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	reader prompts.Reader,
	sessionID string,
	snap api.BoardSnapshot,
	scope packboard.InjectScope,
	omitDelegation bool,
	includeScanLegend bool,
	now time.Time,
) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	data := BuildBoardInjectData(reader, snap, scope, omitDelegation, includeScanLegend, now)
	vars, err := BoardInjectToMap(data)
	if err != nil {
		return "", err
	}
	block, err := anchor.RenderInformWithReader(ctx, anchor.BoardChanged, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, reader, vars)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, BoardOrientationInjectSentinel) {
		return "", fmt.Errorf("board-orientation inject missing sentinel %q", BoardOrientationInjectSentinel)
	}
	return block, nil
}

// RenderWorkerBoardInject renders the worker board Binding (board-orientation template).
func RenderWorkerBoardInject(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	reader prompts.Reader,
	sessionID string,
	snap api.BoardSnapshot,
	scope packboard.InjectScope,
	omitDelegation bool,
	includeScanLegend bool,
	now time.Time,
) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	data := BuildBoardInjectData(reader, snap, scope, omitDelegation, includeScanLegend, now)
	vars, err := BoardInjectToMap(data)
	if err != nil {
		return "", err
	}
	if !snap.Repo.GeneratedAt.IsZero() {
		vars["inventory_at"] = snap.Repo.GeneratedAt.UTC().Format(time.RFC3339)
	}
	block, err := anchor.RenderInformWithReader(ctx, anchor.InjectWorkerBoard, anchor.MatchContext{Surface: "worker", SessionID: sessionID}, renderer, reader, vars)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, BoardOrientationInjectSentinel) {
		return "", fmt.Errorf("board-orientation inject missing sentinel %q", BoardOrientationInjectSentinel)
	}
	return block, nil
}
