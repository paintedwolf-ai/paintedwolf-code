package workercontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// MessageCoordinatorTool is the worker-to-coordinator message tool name.
const MessageCoordinatorTool = "message_coordinator"

// MessageRecords stores a worker's messages.
type MessageRecords interface {
	Self(context.Context, workerrecords.Actor) (workerrecords.Worker, error)
	AppendMessage(context.Context, workerrecords.Actor, workerrecords.Op, string, workerrecords.MessageSpec) (workerrecords.MessageReceipt, error)
}

// MessageCoordinatorDeps wires message_coordinator.
type MessageCoordinatorDeps struct {
	Records MessageRecords
	// NeedsReply wakes the coordinator for a running job's message.
	NeedsReply func(ctx context.Context, parentID, jobID string)
}

// MessageCoordinatorHandler records a message for the coordinator. The worker
// keeps running either way; needs_reply only wakes the coordinator.
func MessageCoordinatorHandler(deps MessageCoordinatorDeps) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if tools.OutOfSessionScope(MessageCoordinatorTool, tctx) {
			return "", &toolrejection.ToolReject{Code: "MESSAGE_COORDINATOR_ADDRESSED_SESSION", Data: map[string]any{"tool": MessageCoordinatorTool}}
		}
		if deps.Records == nil {
			return "", fmt.Errorf("worker records required")
		}
		child := strings.TrimSpace(tctx.Identity.SessionID)
		parent := strings.TrimSpace(tctx.Identity.ParentSessionID)
		callID := strings.TrimSpace(tctx.Identity.ToolCallID)
		if child == "" || parent == "" || callID == "" {
			return "", fmt.Errorf("message_coordinator requires a worker session and tool call")
		}
		text := strings.TrimSpace(stringArg(args, "text"))
		if text == "" {
			return "", fmt.Errorf("text is required")
		}
		needsReply, _ := args["needs_reply"].(bool)
		replyTo, err := replyToArg(args)
		if err != nil {
			return "", err
		}
		actor := workerrecords.Actor{SessionID: child, Role: workerrecords.RoleWorker, PrincipalID: child}
		worker, err := deps.Records.Self(ctx, actor)
		if errors.Is(err, workerrecords.ErrWorkerNotFound) {
			return "", &toolrejection.ToolReject{Code: "MESSAGE_COORDINATOR_NO_WORKER", Data: map[string]any{"tool": MessageCoordinatorTool}}
		}
		if err != nil {
			return "", fmt.Errorf("resolve worker: %w", err)
		}
		receipt, err := deps.Records.AppendMessage(ctx, actor, workerrecords.Op{ID: "message_coordinator:" + child + ":" + callID}, worker.ID,
			workerrecords.MessageSpec{Body: text, ReplyToSeq: replyTo, NeedsReply: needsReply})
		if errors.Is(err, workerrecords.ErrReplyUnknown) {
			return "", &toolrejection.ToolReject{Code: "MESSAGE_COORDINATOR_REPLY_UNKNOWN", Data: map[string]any{"reply_to": replyTo}}
		}
		if err != nil {
			return "", fmt.Errorf("store message: %w", err)
		}
		if needsReply && deps.NeedsReply != nil {
			if job := strings.TrimSpace(tctx.Identity.WorkerJobID); job != "" {
				deps.NeedsReply(ctx, parent, job)
			}
		}
		raw, err := surveyjson.Marshal(map[string]any{"sent": true, "seq": receipt.Seq, "needs_reply": needsReply})
		if err != nil {
			return "", fmt.Errorf("encode message_coordinator ack: %w", err)
		}
		return string(raw), nil
	}
}

func replyToArg(args map[string]any) (int64, error) {
	raw, ok := args["reply_to"]
	if !ok || raw == nil {
		return 0, nil
	}
	n, ok := raw.(float64)
	if !ok || n < 1 || n != float64(int64(n)) {
		return 0, fmt.Errorf("reply_to must be the positive number of a message you received")
	}
	return int64(n), nil
}
