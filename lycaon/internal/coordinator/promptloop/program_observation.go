package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools"
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

func deliveryHandles(deliveries []tools.SourceDeliveryTag) []string {
	handles := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		handles = append(handles, delivery.Handle)
	}
	return handles
}

// noteDeliveryCoverage keeps what each delivery covered until its row is tagged.
func (st *promptLoopTurnState) noteDeliveryCoverage(deliveries []tools.SourceDeliveryTag) {
	if st == nil || len(deliveries) == 0 {
		return
	}
	if st.deliveryCoverage == nil {
		st.deliveryCoverage = make(map[string]string, len(deliveries))
	}
	for _, delivery := range deliveries {
		st.deliveryCoverage[delivery.Handle] = delivery.Coverage
	}
}

// stateDeliveryCoverage writes coverage into a tagged row's leading tags.
func (st *promptLoopTurnState) stateDeliveryCoverage(msg *api.Message) {
	if st == nil || len(st.deliveryCoverage) == 0 {
		return
	}
	msg.Content = withTagCoverage(msg.Content, st.deliveryCoverage)
	if msg.ToolResult != nil {
		msg.ToolResult.Content = withTagCoverage(msg.ToolResult.Content, st.deliveryCoverage)
	}
}

func withTagCoverage(content string, coverage map[string]string) string {
	var tags strings.Builder
	for {
		tag, rest := hostmarker.SplitEvidenceHandleTag(content)
		if tag == "" {
			return tags.String() + content
		}
		handle := hostmarker.EvidenceTagHandle(tag)
		if stated := coverage[handle]; stated != "" {
			tag = "[" + handle + " " + stated + "]\n"
		}
		tags.WriteString(tag)
		content = rest
	}
}
