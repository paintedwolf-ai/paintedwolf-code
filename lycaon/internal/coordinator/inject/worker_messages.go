package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerMessageLine is one delivered message.
type WorkerMessageLine struct {
	Worker      string
	Seq         int64
	ReplyToSeq  int64
	NeedsReply  bool
	Body        string
	ArtifactRef string
}

// WorkerMessagesBlock is a rendered message page: host framing and the
// messages themselves as a separate part, so the bodies keep their sender's
// authority instead of the host's.
type WorkerMessagesBlock struct {
	Header   string
	Messages string
}

// RenderWorkerMessagesBlock renders a page for the coordinator ("coordinator")
// or for a worker ("worker").
func RenderWorkerMessagesBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID, recipient string, lines []WorkerMessageLine, more bool) WorkerMessagesBlock {
	if renderer == nil || len(lines) == 0 {
		return WorkerMessagesBlock{}
	}
	needsReply := false
	for _, line := range lines {
		needsReply = needsReply || line.NeedsReply
	}
	surface := "worker"
	if recipient == "coordinator" {
		surface = "coordinator"
	}
	header, err := anchor.RenderInform(ctx, anchor.InjectWorkerMessages, anchor.MatchContext{Surface: surface, SessionID: sessionID}, renderer, map[string]any{
		"recipient": recipient, "message_count": len(lines), "has_needs_reply": needsReply, "more": more,
	})
	if err != nil || strings.TrimSpace(header) == "" {
		return WorkerMessagesBlock{}
	}
	return WorkerMessagesBlock{Header: strings.TrimSpace(header), Messages: workerMessageData(lines, recipient == "coordinator")}
}

func workerMessageData(lines []WorkerMessageLine, named bool) string {
	var out []string
	for _, line := range lines {
		label := fmt.Sprintf("#%d", line.Seq)
		if named && strings.TrimSpace(line.Worker) != "" {
			label = line.Worker + " " + label
		}
		if line.ReplyToSeq > 0 {
			label += fmt.Sprintf(" (reply to #%d)", line.ReplyToSeq)
		}
		if line.NeedsReply {
			label += " [needs reply]"
		}
		body := strings.TrimSpace(line.Body)
		if ref := strings.TrimSpace(line.ArtifactRef); ref != "" {
			if body != "" {
				body += " "
			}
			body += "[artifact: " + ref + "]"
		}
		out = append(out, label+": "+body)
	}
	return strings.Join(out, "\n")
}

// WorkerMessagesParts places a page in a request: trusted host framing, then
// the messages. A worker's words reach its coordinator as untrusted data; a
// coordinator's instructions reach its worker with the coordinator's
// authority over that worker.
func WorkerMessagesParts(block WorkerMessagesBlock, recipient string) []api.MessageContentPart {
	if block.Header == "" {
		return nil
	}
	parts := []api.MessageContentPart{{
		Content: block.Header, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
	}}
	data := api.MessageContentPart{Content: block.Messages, Origin: api.MessageOriginPeerAgent, Source: "worker_messages"}
	if recipient == "coordinator" {
		data.Authority, data.TrustTier = api.ContentAuthorityNone, api.ContentTrustTierUntrusted
	} else {
		data.Authority, data.TrustTier = api.ContentAuthorityUser, api.ContentTrustTierTrusted
	}
	return append(parts, data)
}
