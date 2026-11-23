import { Show, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import { reviewPendingApproval } from "../../../chat/checkpoint/approval-review.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  TranscriptDisclosureSummary,
  TranscriptDisclosureToggle,
  useTranscriptDisclosure,
} from "../../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../../chat/transcript/presentation/transcript-disclosure-key.ts";
import type { ActivityGroupApproval } from "../../../chat/transcript/projection/transcript-item-model.ts";
import { bindFindRevealHost } from "../../../find/use-findable-view.ts";
import {
  CheckpointDecisionChicklet,
  checkpointDecisionLabel,
} from "../../checkpoint/CheckpointDecisionChicklet.tsx";
import { KeyedIndex } from "../../keyed-index.tsx";
import { DecisionRow, decisionRowLabel } from "../DecisionRow.tsx";

type Props = {
  groupKey: string;
  approvals: readonly ActivityGroupApproval[];
  sessionId?: string | null;
  projectId?: string;
  client?: LycaonClient | null;
  onShowActivity?: (toolCallId: string) => void;
  onError?: (err: unknown) => void;
};

function resolvedApprovalsHint(approvals: readonly ActivityGroupApproval[]): { display: string; full: string } {
  const parts = approvals.map((approval) => {
    const label = checkpointDecisionLabel(approval.meta);
    const target = approval.meta.tool?.trim() || approval.meta.subject?.trim();
    return target ? `${label} ${target}` : label;
  });
  const full = parts.join(" · ");
  const shown = parts.slice(0, 2);
  const extra = parts.length - shown.length;
  const display = extra > 0 ? `${shown.join(" · ")} · +${extra}` : shown.join(" · ");
  return { display, full };
}

function approvalsCorpus(approvals: readonly ActivityGroupApproval[]): string {
  return approvals
    .flatMap((a) => [
      checkpointDecisionLabel(a.meta),
      a.meta.tool,
      a.meta.subject,
      a.meta.location,
      a.meta.causing_command,
      a.meta.grant_title,
      a.meta.guidance,
    ])
    .filter(Boolean)
    .join("\n");
}

export function ActivityGroupApprovals(props: Props) {
  const disclosure = useTranscriptDisclosure(() =>
    transcriptDisclosureKey.activityGroupApprovals(props.groupKey),
  );
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);

  bindFindRevealHost({
    id: `activity-group-approvals-reveal:${props.groupKey}`,
    hostEl,
    isCollapsed: () => props.approvals.length > 1 && !disclosure.open(),
    revealForFind: disclosure.revealTemporarily,
    collapsedCorpus: () => (props.approvals.length > 1 ? approvalsCorpus(props.approvals) : ""),
    collapsedCorpusAnchor: hostEl,
  });

  const firstPending = createMemo(() =>
    props.approvals.find((approval) => approval.meta.status === "pending"),
  );
  const resolvedHint = createMemo(() => resolvedApprovalsHint(props.approvals));

  return (
    <Show
      when={props.approvals.length > 1}
      fallback={
        <Show when={props.approvals[0]} keyed>
          {(solo) => (
            <CheckpointDecisionChicklet
              chrome="section"
              meta={solo.meta}
              sessionId={props.sessionId}
              projectId={props.projectId}
              entryKey={solo.key}
              toolCallId={solo.toolCallId ?? solo.call?.toolCallId}
              onShowActivity={props.onShowActivity}
              client={props.client}
              onError={props.onError}
            />
          )}
        </Show>
      }
    >
      <TranscriptDisclosureShell
        disclosure={disclosure}
        ref={setHostEl}
        class="den-activity-group-approvals den-transcript-disclosure-card"
        data-testid="activity-group-approvals"
        data-count={props.approvals.length}
        data-status={firstPending() ? "pending" : "settled"}
      >
        <Show when={firstPending()} keyed>
          {(pending) => (
            <span
              data-testid="checkpoint-open-marker"
              data-checkpoint-id={pending.meta.checkpoint_id}
              class="sr-only"
            />
          )}
        </Show>
        <TranscriptDisclosureSummary
          disclosure={disclosure}
          class="den-transcript-disclosure-header den-checkpoint-decision-chicklet__summary"
        >
          <TranscriptDisclosureToggle
            disclosure={disclosure}
            class="den-checkpoint-decision-chicklet__summary-main"
            data-testid="activity-group-approvals-toggle"
            aria-label={(() => {
              const pending = firstPending();
              return pending
                ? decisionRowLabel(checkpointDecisionLabel(pending.meta), [
                    pending.meta.tool,
                    pending.meta.subject,
                    `${props.approvals.length - 1} more`,
                  ])
                : `${props.approvals.length} approvals, ${resolvedHint().full}`;
            })()}
          >
            <Show
              when={firstPending()}
              keyed
              fallback={
                <span class="den-activity-group-approvals__resolved">
                  <span class="den-activity-group-approvals__count" data-testid="approvals-count">
                    {props.approvals.length} approvals
                  </span>
                  <span
                    class="den-activity-group-approvals__hint"
                    data-testid="approvals-hint"
                    data-tip={resolvedHint().full}
                    data-tip-when-clipped=""
                  >
                    {resolvedHint().display}
                  </span>
                </span>
              }
            >
              {(pending) => (
                <DecisionRow
                  tone="pending"
                  label={checkpointDecisionLabel(pending.meta)}
                  details={[
                    { text: pending.meta.tool ?? "", testId: "approvals-pending-tool" },
                    {
                      text:
                        pending.meta.subject && pending.meta.subject !== pending.meta.tool
                          ? pending.meta.subject
                          : "",
                      emphasis: "subject",
                      truncate: true,
                      testId: "approvals-pending-subject",
                    },
                    { text: `+${props.approvals.length - 1} more`, testId: "approvals-pending-more" },
                  ]}
                />
              )}
            </Show>
          </TranscriptDisclosureToggle>
          <Show when={firstPending() && props.sessionId}>
            <button
              type="button"
              class="den-checkpoint-decision-chicklet__review-btn"
              data-testid="approvals-review-dock"
              data-disclosure-action=""
              onClick={() => {
                const pending = firstPending();
                if (pending && props.sessionId) {
                  reviewPendingApproval({
                    sessionId: props.sessionId,
                    checkpointId: pending.meta.checkpoint_id,
                  });
                }
              }}
            >
              Review in composer
            </button>
          </Show>
          <span class="den-tool-chicklet-caret" aria-hidden="true" />
        </TranscriptDisclosureSummary>
        <TranscriptDisclosurePanel disclosure={disclosure} class="den-activity-group-approvals__list">
          <KeyedIndex each={props.approvals} keyOf={(approval) => approval.key}>
            {(approval) => (
              <CheckpointDecisionChicklet
                chrome="row"
                meta={approval().meta}
                sessionId={props.sessionId}
                projectId={props.projectId}
                entryKey={approval().key}
                toolCallId={approval().toolCallId ?? approval().call?.toolCallId}
                onShowActivity={props.onShowActivity}
                client={props.client}
                onError={props.onError}
              />
            )}
          </KeyedIndex>
        </TranscriptDisclosurePanel>
      </TranscriptDisclosureShell>
    </Show>
  );
}
