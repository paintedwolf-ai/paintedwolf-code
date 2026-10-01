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
import { ApprovalCard, effectiveConsequence, isHighRiskBand } from "./ApprovalCard.tsx";
import { setDockVisibleCheckpointId } from "../../chat/checkpoint/redirect-target.ts";
import { projectMatchesDir } from "../../api/project-path.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ContentReviewRule } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { pendingCheckpointsForSession } from "../../chat/actions/chat-actions.ts";
import {
  resolveContentApply,
  resolveToolApproval,
} from "../../chat/checkpoint/checkpoint-actions.ts";
import {
  composerDraftForSession,
  flushComposerDraftsToDisk,
} from "../../chat/composer/composer-drafts.ts";
import { pendingAttachmentsForSession } from "../../chat/composer/composer-attachment-store.ts";
import { promptPartsFromPendingAttachments } from "../../chat/composer/prompt-parts.ts";
import {
  removeComposerDocumentAttachment,
  replaceComposerDocumentDraft,
} from "../../chat/composer/composer-document-store.ts";
import { KeyedIndex } from "../keyed-index.tsx";

/** Review rules match an exact path or a directory subtree. */
function reviewRuleMatches(
  rule: ContentReviewRule,
  tool: string,
  path: string,
): boolean {
  if (rule.tool && rule.tool !== tool) return false;
  const p = rule.path.trim();
  if (p === "" || p === "*" || p === "**") return true;
  if (p.endsWith("/**")) {
    const prefix = p.slice(0, -3);
    return path === prefix || path.startsWith(prefix + "/");
  }
  return p === path;
}

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  appStore: AppStore;
  projects: ProjectsStore;
  client: LycaonClient;
  sessionId: string;
  projectDir: string;
};

/** Bounds approval queue height. */
const MAX_QUEUE_ROWS = 3;

function checkpointHighRisk(checkpoint: PendingCheckpoint): boolean {
  return isHighRiskBand(
    effectiveConsequence(checkpoint.tool_approval).consequence_band,
  );
}

export function CheckpointCards(props: Props) {
  const [resolving, setResolving] = createSignal<string | null>(null);
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

  // The dock slot measures its exit after the last checkpoint leaves the store, so the
  // resolved card stays rendered, inert, until the slot unmounts it.
  const shown = createMemo<PendingCheckpoint | undefined>((held) => expanded() ?? held);
  const retired = () => expanded() === undefined;

  const expandedRows = createMemo(() => {
    const checkpoint = shown();
    return checkpoint ? [checkpoint] : [];
  });

  // Composer Send targets the visible card.
  createEffect(() => {
    setDockVisibleCheckpointId(expanded()?.checkpointId ?? null);
  });
  onCleanup(() => setDockVisibleCheckpointId(null));

  const notices = useNotices();

  const handleError = (err: unknown) => {
    notices.reportError(err);
  };

  const skipReviewPath = async (
    checkpoint: PendingCheckpoint,
    mode: "day" | "always",
  ) => {
    const path = checkpoint.content_apply?.path?.trim();
    const tool = checkpoint.content_apply?.tool?.trim() || "write";
    if (!path) return;
    const project = props.projects.state.projects.find((p) =>
      projectMatchesDir(p, props.projectDir),
    );
    const projectId = project?.id;
    if (!projectId) return;
    setResolving(checkpoint.checkpointId);
    try {
      const current = await props.client.getReviewSettings(projectId);
      const rules = current.review_paths ?? [];
      const review_paths =
        mode === "always"
          ? rules.filter((r) => !reviewRuleMatches(r, tool, path))
          : rules.map((r) =>
              reviewRuleMatches(r, tool, path)
                ? {
                    ...r,
                    disabled_until: new Date(
                      Date.now() + 24 * 60 * 60 * 1000,
                    ).toISOString(),
                  }
                : r,
            );
      await props.client.updateReviewSettings({ review_paths }, projectId);
      await resolveContentApply(props.client, checkpoint, "approve");
    } catch (err) {
      handleError(err);
    } finally {
      setResolving(null);
    }
  };

  // Only an explicit No attaches composer guidance.
  const captureGuidance = (withGuidance?: boolean) => {
    if (!withGuidance) return { draft: "", secrets: [], secretAttachmentIds: [] };
    const secretAttachments = pendingAttachmentsForSession(props.sessionId).filter(
      (attachment) => attachment.kind === "secret",
    );
    return {
      draft: composerDraftForSession(props.sessionId),
      secrets: promptPartsFromPendingAttachments(secretAttachments).secrets,
      secretAttachmentIds: secretAttachments.map((attachment) => attachment.id),
    };
  };

  const onToolApproval = async (
    checkpoint: PendingCheckpoint,
    action: "approve" | "reject",
    options?: { optionId?: string; withGuidance?: boolean },
  ) => {
    const captured = action === "reject" ? captureGuidance(options?.withGuidance) : captureGuidance();
    const guidance = captured.draft.trim();
    setResolving(checkpoint.checkpointId);
    try {
      await resolveToolApproval(
        props.client,
        checkpoint,
        action,
        {
          optionId: options?.optionId,
          guidance: guidance || undefined,
          secrets: captured.secrets,
        },
      );
      await consumeAttachedGuidance(captured);
    } catch (err) {
      handleError(err);
    } finally {
      setResolving(null);
    }
  };

  const consumeAttachedGuidance = async (captured: ReturnType<typeof captureGuidance>) => {
    if (!captured.draft.trim() && captured.secretAttachmentIds.length === 0) return;
    if (composerDraftForSession(props.sessionId) !== captured.draft) return;
    const destinationProjectId = projectId();
    if (!destinationProjectId) return;
    await replaceComposerDocumentDraft(
      { projectId: destinationProjectId, sessionId: props.sessionId },
      "",
    );
    for (const attachmentId of captured.secretAttachmentIds) {
      await removeComposerDocumentAttachment(
        { projectId: destinationProjectId, sessionId: props.sessionId },
        attachmentId,
      );
    }
    await flushComposerDraftsToDisk();
  };

  const onContentApply = async (
    checkpoint: PendingCheckpoint,
    body: {
      decision: "approve" | "reject" | "approve_partial";
      approvedHunks?: string[];
      withGuidance?: boolean;
    },
  ) => {
    const captured = body.decision === "reject" ? captureGuidance(body.withGuidance) : captureGuidance();
    const guidance = captured.draft.trim();
    setResolving(checkpoint.checkpointId);
    try {
      await resolveContentApply(
        props.client,
        checkpoint,
        body.decision,
        {
          approvedHunks: body.approvedHunks,
          guidance: guidance || undefined,
          secrets: captured.secrets,
        },
      );
      await consumeAttachedGuidance(captured);
    } catch (err) {
      handleError(err);
    } finally {
      setResolving(null);
    }
  };

  const projectId = () =>
    props.projects.state.projects.find((p) =>
      projectMatchesDir(p, props.projectDir),
    )?.id;

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
            resolving={retired() || resolving() === checkpoint().checkpointId}
            inert={retired()}
            onToolApproval={(action, options) =>
              void onToolApproval(checkpoint(), action, options)
            }
            onContentApply={(body) => void onContentApply(checkpoint(), body)}
            onSkipReviewPath={
              checkpoint().kind === "content_apply"
                ? (mode) => void skipReviewPath(checkpoint(), mode)
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
