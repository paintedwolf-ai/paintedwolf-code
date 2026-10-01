import { Show } from "solid-js";
import type { WorkflowFeedbackMeta } from "../../api/types.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { DecisionRow } from "../transcript/DecisionRow.tsx";
import {
  type WorkflowFeedbackCardView,
  workflowFeedbackCardHint,
  workflowFeedbackCardLabel,
  workflowFeedbackChickletDetail,
} from "../../workflow/workflow-feedback-card-model.ts";

type Props = {
  meta: WorkflowFeedbackMeta;
  view: WorkflowFeedbackCardView;
  answer: string;
  sessionId?: string | null;
  entryKey?: string;
};

export function WorkflowFeedbackChicklet(props: Props) {
  const entryOpts = () =>
    props.entryKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey }
      : undefined;
  const { bindTranscriptEntry } = useTranscriptEntry(entryOpts);
  const { key: disclosureKey, open, onToggle, onSummaryClick } = useTranscriptDisclosure(() =>
    props.entryKey ? transcriptDisclosureKey.workflowFeedback(props.entryKey) : undefined,
  );

  const label = () => workflowFeedbackCardLabel(props.view);
  const hint = () => workflowFeedbackCardHint(props.view);
  const detail = () =>
    workflowFeedbackChickletDetail(props.meta.prompt, props.answer);
  const resolvedBy = () =>
    props.view.kind === "answered" ? props.view.resolvedBy : props.meta.resolved_by;
  const ariaLabel = () => {
    const parts = [label()];
    const d = detail();
    if (d) parts.push(d);
    return parts.join(" · ");
  };
  const statusClass = () => ({
    "den-checkpoint-decision-chicklet--approved": props.view.kind === "answered",
    "den-checkpoint-decision-chicklet--expired": props.view.kind === "closed",
  });

  return (
    <details
      ref={bindTranscriptEntry}
      class="den-checkpoint-decision-chicklet den-checkpoint-decision-chicklet--expandable den-transcript-disclosure-card"
      classList={statusClass()}
      data-testid="workflow-feedback-chicklet"
      data-card-state={props.view.kind}
      data-resolved-by={resolvedBy() || undefined}
      data-phase-id={props.meta.phase_id}
      data-disclosure-key={disclosureKey}
      open={open()}
      onToggle={onToggle}
    >
      <summary
        class="den-checkpoint-decision-chicklet__summary"
        onClick={onSummaryClick}
        aria-label={ariaLabel()}
      >
        <DecisionRow
          class="den-checkpoint-decision-chicklet__summary-main"
          tone="neutral"
          label={label()}
          details={[
            {
              text: detail() ?? "",
              emphasis: "subject",
              truncate: true,
              testId: "workflow-feedback-chicklet-detail",
            },
          ]}
        />
        <span class="den-tool-chicklet-caret" aria-hidden="true" />
      </summary>
      <div
        class="den-checkpoint-decision-chicklet__body"
        data-testid="workflow-feedback-chicklet-body"
      >
        <Show when={(props.meta.prompt ?? "").trim()}>
          {(prompt) => (
            <p class="den-workflow-feedback-chicklet__prompt" data-testid="workflow-feedback-chicklet-prompt">
              {prompt()}
            </p>
          )}
        </Show>
        <Show when={props.answer.trim()}>
          {(answer) => (
            <p class="den-workflow-feedback-chicklet__answer" data-testid="workflow-feedback-answer">
              <span class="den-workflow-feedback-chicklet__answer-label">
                Your answer
              </span>
              {answer()}
            </p>
          )}
        </Show>
        <Show when={hint()}>
          {(h) => (
            <p class="den-workflow-feedback-chicklet__hint" data-testid="workflow-feedback-closed-hint">
              {h()}
            </p>
          )}
        </Show>
      </div>
    </details>
  );
}
