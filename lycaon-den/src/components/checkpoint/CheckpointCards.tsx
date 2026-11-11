import { useNotices } from "../../notices/notice-reporter.tsx";
import {
  createEffect,
  createMemo,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { checkpointRowLabel } from "./checkpoint-row-label.ts";
import { revealApprovalOrigin } from "../../chat/checkpoint/approval-reveal.ts";
import { approvalReviewTarget, consumeApprovalReview } from "../../chat/checkpoint/approval-review.ts";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";
import { effectiveConsequence, isHighRiskBand } from "./approval-consequence.ts";
import { setDockVisibleCheckpointId } from "../../chat/checkpoint/redirect-target.ts";
import { createCheckpointDecisions } from "./checkpoint-decisions.ts";
import { projectMatchesDir } from "../../api/project-path.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { pendingCheckpointsForSession } from "../../chat/actions/chat-actions.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { createResolvedFocusReturn } from "./resolved-focus-return.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  appStore: AppStore;
  projects: ProjectsStore;
  client: LycaonClient;
  sessionId: string;
  projectDir: string;
  /** Focus left with a resolved card; the chat returns it to its composer. */
  onFocusReturn?: () => void;
};

/** Bounds approval queue height. */
const MAX_QUEUE_ROWS = 3;

function checkpointHighRisk(checkpoint: PendingCheckpoint): boolean {
  return isHighRiskBand(
    effectiveConsequence(checkpoint.tool_approval).consequence_band,
  );
}

export function CheckpointCards(props: Props) {
  const [requestedId, setRequestedId] = createSignal<string | null>(null);
  const [minimized, setMinimized] = createSignal(false);
  let seenIds = new Set<string>();

  const rows = () =>
    pendingCheckpointsForSession(props.appStore, props.sessionId);

  createEffect(() => {
    const target = approvalReviewTarget();
    if (!target || target.sessionId !== props.sessionId || !rows().some(row => row.checkpointId === target.checkpointId)) return;
    setRequestedId(target.checkpointId);
    setMinimized(false);
    let stopped = false;
    const focusReview = () => {
      if (stopped || approvalReviewTarget() !== target) return;
      const card = [...document.querySelectorAll<HTMLElement>(".den-approval-card[data-checkpoint-id]")]
        .find(element => element.dataset.checkpointId === target.checkpointId
          && !element.closest('[inert], [hidden], [aria-hidden="true"]'));
      if (focusWithoutScroll(card)) consumeApprovalReview(target);
    };
    // Retained panes remain inert during the opening transition.
    const observer = new MutationObserver(focusReview);
    observer.observe(document.body, { childList: true, subtree: true, attributes: true,
      attributeFilter: ["inert", "hidden", "aria-hidden", "class", "style"] });
    const timeout = setTimeout(() => consumeApprovalReview(target), 6000);
    queueMicrotask(focusReview);
    onCleanup(() => {
      stopped = true;
      observer.disconnect();
      clearTimeout(timeout);
    });
  });

  // Prefer the requested pending card, then arrival order.
  const expanded = createMemo(() => {
    const list = rows();
    const requested = requestedId();
    return (
      (requested
        ? list.find((checkpoint) => checkpoint.checkpointId === requested)
        : undefined) ?? list[0]
    );
  });

  // Track arrivals without making the previous IDs a reactive dependency.
  createEffect(() => {
    const ids = new Set(rows().map((row) => row.checkpointId));
    let arrived = false;
    for (const id of ids) {
      if (!seenIds.has(id)) arrived = true;
    }
    const hadSeen = seenIds.size > 0;
    seenIds = ids;
    if (arrived && hadSeen) setMinimized(false);
  });

  const queued = createMemo(() =>
    rows().filter(
      (checkpoint) =>
        checkpoint.checkpointId !== expanded()?.checkpointId,
    ),
  );

  // The inert card remains mounted while the dock measures its exit.
  const shown = createMemo<PendingCheckpoint | undefined>((held) => expanded() ?? held);
  const retired = () => expanded() === undefined;

  const expandedRows = createMemo(() => {
    const checkpoint = shown();
    return checkpoint ? [checkpoint] : [];
  });

  const focusReturn = createResolvedFocusReturn(() => props.onFocusReturn?.());
  createEffect(() => {
    expandedRows();
    const done = retired();
    queueMicrotask(() => focusReturn.settle(done));
  });
  onCleanup(() => focusReturn.settle(true));

  // Composer Send targets the visible card.
  createEffect(() => {
    setDockVisibleCheckpointId(expanded()?.checkpointId ?? null);
  });
  onCleanup(() => setDockVisibleCheckpointId(null));

  const projectId = () =>
    props.projects.state.projects.find((p) =>
      projectMatchesDir(p, props.projectDir),
    )?.id;

  const notices = useNotices();

  const decisions = createCheckpointDecisions({
    client: props.client,
    sessionId: () => props.sessionId,
    projectId,
    onStart: (id) => focusReturn.note(id),
    reportError: (error) => notices.reportError(error),
  });
  createEffect(() => decisions.retain(rows().map((checkpoint) => checkpoint.checkpointId)));

  return (
    <>
      <Show when={queued().length > 0}>
        <div
          class="den-approval-queue"
          data-testid="checkpoint-queue"
          role="group"
          aria-label={APPROVALS_COPY.card.queue.ariaLabel}
        >
          <For each={queued().slice(0, MAX_QUEUE_ROWS)}>
            {(checkpoint) => (
              <button
                type="button"
                class="den-approval-queue-row"
                data-testid="checkpoint-queue-row"
                data-checkpoint-id={checkpoint.checkpointId}
                onClick={() => {
                  setMinimized(false);
                  setRequestedId(checkpoint.checkpointId);
                }}
              >
                <Show when={checkpointHighRisk(checkpoint)}>
                  <span
                    class="den-approval-queue-risk"
                    data-testid="checkpoint-queue-risk"
                  >
                    {APPROVALS_COPY.card.highRisk.label}
                  </span>
                </Show>
                <span class="den-approval-queue-label">
                  {checkpointRowLabel(checkpoint)}
                  <Show when={decisions.notice(checkpoint.checkpointId)} keyed>
                    {(notice) => <span data-testid="checkpoint-queue-error"> · {notice.title}</span>}
                  </Show>
                </span>
              </button>
            )}
          </For>
          <Show when={queued().length > MAX_QUEUE_ROWS}>
            <p class="den-approval-dock-more" data-testid="checkpoint-dock-more">
              {APPROVALS_COPY.card.dockMore(queued().length - MAX_QUEUE_ROWS)}
            </p>
          </Show>
        </div>
      </Show>
      {/* Checkpoint identity controls disclosure and menu state. */}
      <KeyedIndex
        each={expandedRows()}
        keyOf={(checkpoint) => checkpoint.checkpointId}
      >
        {(checkpoint) => (
          <ApprovalCard
            client={props.client}
            checkpoint={checkpoint()}
            sessionId={props.sessionId}
            projectId={projectId()}
            resolving={retired() || decisions.pending(checkpoint().checkpointId)}
            inert={retired()}
            decisionNotice={retired() ? undefined : decisions.notice(checkpoint().checkpointId)}
            onToolApproval={(action, options) =>
              void decisions.onToolApproval(checkpoint(), action, options)
            }
            onContentApply={(body) => void decisions.onContentApply(checkpoint(), body)}
            onSkipReviewPath={
              checkpoint().kind === "content_apply"
                ? (mode) => void decisions.skipReviewPath(checkpoint(), mode)
                : undefined
            }
            minimized={minimized()}
            onMinimizedChange={setMinimized}
            onShowInChat={(toolCallId) => {
              setMinimized(true);
              revealApprovalOrigin({
                toolCallId,
                sessionId: checkpoint().sessionId,
              });
            }}
          />
        )}
      </KeyedIndex>
    </>
  );
}
