import { Show, createMemo } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { WorkflowFeedbackMeta } from "../../api/types.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { workflowFeedbackCardView } from "../../workflow/workflow-feedback-card-model.ts";
import { WorkflowFeedbackChicklet } from "./WorkflowFeedbackChicklet.tsx";

type Props = {
  meta: WorkflowFeedbackMeta;
  runId?: string;
  sessionId?: string | null;
  entryKey?: string;
  client?: LycaonClient | null;
  /** Optimistic answer stamp so remounts / span prebuilds collapse without waiting for SSE. */
  appStore?: AppStore | null;
  /** Current pending phase closes superseded cards. `null` means none; `undefined` means unknown. */
  activePendingPhaseId?: string | null;
};

/** Transcript presentation for workflow feedback; active forms render in AskUserDock. */
export function WorkflowFeedbackCard(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.entryKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey }
      : undefined,
  );
  const answered = () => (props.meta.answer ?? "").trim();
  const view = createMemo(() =>
    workflowFeedbackCardView(props.meta, props.activePendingPhaseId),
  );

  return (
    <Show
      when={view().kind === "open"}
      fallback={
        <WorkflowFeedbackChicklet
          meta={props.meta}
          view={view()}
          answer={answered()}
          sessionId={props.sessionId}
          entryKey={props.entryKey}
        />
      }
    >
      <div
        ref={(el) => bindTranscriptEntry(el)}
        class="den-ask-user-open-marker"
        data-testid="workflow-feedback-open-marker"
        data-phase-id={props.meta.phase_id}
        role="status"
      >
        Waiting for your answer in the composer…
      </div>
    </Show>
  );
}
