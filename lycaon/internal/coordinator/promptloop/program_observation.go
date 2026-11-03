package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func (l *toolBatch) tagProgramObservation(ctx context.Context, sessionID, tool string, msg *api.Message) error {
	if l.Tools.Deps.CommitProgramObservation != nil {
		content := msg.ToolResult.Content
		if strings.TrimSpace(content) == "" {
			content = msg.Content
		}
		handle, err := l.Tools.Deps.CommitProgramObservation(ctx, sessionID, tool, msg.ToolResult.CommandProgram, content)
		if err != nil {
			return fmt.Errorf("record %s program observation: %w", tool, err)
		}
		if handle != "" {
			msg.EvidenceHandles = append(msg.EvidenceHandles, handle)
		}
	}
	msg.Content = prependEvidenceHandles(msg.Content, msg.EvidenceHandles)
	msg.ToolResult.Content = prependEvidenceHandles(msg.ToolResult.Content, msg.EvidenceHandles)
	return nil
}
