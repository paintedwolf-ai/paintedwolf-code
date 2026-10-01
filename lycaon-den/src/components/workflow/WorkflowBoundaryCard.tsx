import type { WorkflowBoundaryView } from "../../chat/workflow/workflow-boundary-label.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";

type Props = {
  boundary: WorkflowBoundaryView;
  sessionId?: string | null;
};

export function WorkflowBoundaryCard(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: props.boundary.messageId,
  }));

  return (
    <div
      ref={bindTranscriptEntry}
      class="den-workflow-boundary"
      data-testid="workflow-boundary"
      data-message-id={props.boundary.messageId}
    >
      <span class="den-workflow-boundary-line" aria-hidden="true" />
      <span class="den-workflow-boundary-label">{props.boundary.label}</span>
      <span class="den-workflow-boundary-line" aria-hidden="true" />
    </div>
  );
}
