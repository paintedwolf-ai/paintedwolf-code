import { Show, createEffect, createMemo, createSignal, on } from "solid-js";
import { composerAttachFocusPulse } from "../../chat/composer/add-to-chat.ts";
import { isChatActivityLive } from "../../chat/session/session-activity.ts";
import { pendingCheckpointsForSession } from "../../chat/actions/chat-actions.ts";
import { resolveContentApply, resolveToolApproval } from "../../chat/checkpoint/checkpoint-actions.ts";
import { dockVisibleCheckpointId, lastFocusedCheckpointId, pickRedirectTarget } from "../../chat/checkpoint/redirect-target.ts";
import { getLycaonClient, type noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { QueuePopover } from "./QueuePopover.tsx";
import { Composer } from "./Composer.tsx";
import { ComposerChromeStack } from "./ComposerChromeStack.tsx";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { SessionWorkflowLauncher } from "./SessionWorkflowLauncher.tsx";
import { CheckpointCards } from "../checkpoint/CheckpointCards.tsx";
import { AskUserDock } from "../workflow/AskUserDock.tsx";
import type { AskUserDockSubmitFn } from "../workflow/AskUserDock.tsx";
import { askUserDockMeta, pendingAskFeedbackEntry, pendingWorkflowFeedback, retainAskDockMeta, workflowFeedbackComposerPlaceholder } from "../../workflow/workflow-feedback-model.ts";
import { clearUnreadBoundary } from "../../attention/unread-boundary.ts";
import { armedComposerPlaceholder, armedWorkflowFromPickerRow, resolveComposerWorkflowStart } from "../../chat/workflow/workflow-arm.ts";
import { clearPendingArmWorkflow, peekArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import { closeFind } from "../../find/find-controller.ts";
import type { Accessor } from "solid-js";
import type { ChatViewProps } from "./chat-view-props.ts";
import type { createChatViewActivity } from "./chat-view-activity.ts";
import type { createChatWorkflowState } from "./chat-workflow-state.ts";
import type { createChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import type { createQueueController } from "./queue-controller.ts";
import type { createTranscriptViewportController } from "../../chat/stream/transcript-viewport.tsx";
import type { ChatTabChromeValue } from "../../chat/composer/chat-tab-chrome.tsx";

type ComposerOptions = {
  activity: ReturnType<typeof createChatViewActivity>;
  viewport: ReturnType<typeof createTranscriptViewportController>;
  chrome: ChatTabChromeValue;
  workflow: ReturnType<typeof createChatWorkflowState>;
  blueprint: ReturnType<typeof createChatBlueprintState>;
  queue: ReturnType<typeof createQueueController>;
  surfaceActive: Accessor<boolean>;
  composerRefocus: Accessor<number>;
  composerRehydrate: Accessor<number>;
  clientOrThrow: () => NonNullable<ReturnType<typeof getLycaonClient>>;
  connectedClient: () => ReturnType<typeof getLycaonClient>;
  chatNotices: () => ReturnType<typeof noticeReporterFor>;
};

export function createChatViewComposer(props: ChatViewProps, options: ComposerOptions) {
  const { viewport, chrome, workflow, blueprint, queue, surfaceActive, composerRefocus, composerRehydrate, clientOrThrow, connectedClient, chatNotices } = options;
  const { activeRun, catalogRun, chatLive, stopping, thinkingLabel, pauseHint, launcherTiles, showSessionLauncher, sessionHoldsExternalContent } = options.activity;
  const [askRefocusPulse, setAskRefocusPulse] = createSignal(0);
  let askDockSubmit: AskUserDockSubmitFn | undefined;
  const [askAnswerReady, setAskAnswerReady] = createSignal(false);
  /** Keeps minimized state across keyed dock mounts. */
  const [askMinimized, setAskMinimized] = createSignal(false);

  const pendingAsk = createMemo(() => pendingWorkflowFeedback(activeRun()));
  const pendingAskEntry = createMemo(() =>
    pendingAskFeedbackEntry(
      props.appStore.state.messages,
      pendingAsk()?.phase_id,
    ),
  );
  const pendingAskRun = createMemo(() => {
    const runId = pendingAskEntry()?.runId ?? activeRun()?.id;
    if (!runId) return undefined;
    return (
      props.appStore.state.workflowRuns.find((run) => run.id === runId) ??
      (activeRun()?.id === runId ? activeRun() : undefined)
    );
  });
  const pendingAskMeta = createMemo(() => {
    const pending = pendingAsk();
    if (!pending) return undefined;
    return askUserDockMeta(pending, pendingAskEntry()?.meta);
  });
  // A queued send waits while its tool approval is parked.
  const sendWaitingOnApproval = createMemo(
    () =>
      (props.appStore.state.queueDraft?.sending ?? false) &&
      pendingCheckpointsForSession(props.appStore, props.sessionId).some(
        (cp) => cp.kind === "tool_approval",
      ),
  );

  const approvalRedirectCheckpoint = createMemo(() =>
    pickRedirectTarget(
      pendingCheckpointsForSession(props.appStore, props.sessionId),
      lastFocusedCheckpointId(),
      dockVisibleCheckpointId(),
    ),
  );
  /** Phase identity preserves dock-local answers. */
  const askDockPhaseId = createMemo(() => {
    const phaseId = (pendingAsk()?.phase_id ?? "").trim();
    if (!phaseId || !props.sessionId || !getLycaonClient()) return null;
    return phaseId;
  });
  const retainedPendingAskMeta = createMemo<
    ReturnType<typeof pendingAskMeta>
  >((previous) =>
    retainAskDockMeta(previous, pendingAskMeta(), askDockPhaseId()),
  );

  createEffect(
    on(
      () => pendingAsk()?.phase_id,
      (phase, prev) => {
        if (!phase || phase === prev) return;
        // The ask controls composer focus.
        closeFind();
        setAskRefocusPulse((n) => n + 1);
        setAskMinimized(false);
      },
    ),
  );

  createEffect(() => {
    const sessionId = props.sessionId;
    const pendingWf = peekArmWorkflow(sessionId);
    if (!pendingWf) return;
    const next = armedWorkflowFromPickerRow(pendingWf);
    if (!next) return;
    const current = workflow.armed();
    if (
      current &&
      current.workflow_id === next.workflow_id &&
      (current.preset_id ?? "") === (next.preset_id ?? "")
    ) {
      return;
    }
    workflow.setArmed(next);
  });

  const sendPromptWithStreamFollow = async (
    payload: import("./Composer.tsx").ComposerSendPayload,
  ): Promise<boolean | void> => {
    return props.onSend({
      ...payload,
      onPendingSend: (destination) => {
        payload.onPendingSend?.(destination);
        if (destination === "transcript") {
          clearUnreadBoundary(props.sessionId);
          viewport.jumpToTail(false);
        }
      },
    });
  };

  const sendWithStreamFollow = async (
    payload: import("./Composer.tsx").ComposerSendPayload,
  ): Promise<boolean | void> => {
    if (payload.recovery) return sendPromptWithStreamFollow(payload);
    const pending = pendingAsk();
    if (pending) {
      // The draft survives while the question dock mounts.
      if (!askDockSubmit) return false;
      if (!(await askDockSubmit(payload.text ?? "", payload.secrets))) return false;
      viewport.resumeFollowing();
      return;
    }

    // Composer direction rejects the active approval.
    const approval = approvalRedirectCheckpoint();
    if (approval) {
      const direction = payload.text?.trim() ?? "";
      if (!direction && !payload.secrets?.length) return false;
      const client = getLycaonClient();
      if (!client) return false;
      const denyWithDirection = (): Promise<void> => {
        switch (approval.kind) {
          case "content_apply":
            return resolveContentApply(client, approval, "reject", {
              guidance: direction,
              secrets: payload.secrets,
            });
          default:
            return resolveToolApproval(client, approval, "reject", {
              guidance: direction,
              secrets: payload.secrets,
            });
        }
      };
      try {
        await denyWithDirection();
        viewport.resumeFollowing();
        return;
      } catch (err) {
        chatNotices().reportError(err);
        return false;
      }
    }

    const text = payload.text?.trim() ?? "";
    const armed = workflow.armed();

    const resolution = resolveComposerWorkflowStart({
      text,
      catalog: props.appStore.state.workflowCatalog,
      armed,
    });
    if (resolution.kind === "arm") {
      workflow.setArmed(resolution.armed);
      return;
    }
    workflow.clearArmed();
    clearPendingArmWorkflow();
    return sendPromptWithStreamFollow({ ...payload, text: resolution.text });
  };

  return { sendWithStreamFollow, resetAsk: () => setAskMinimized(false), render: () => (
          <ComposerChromeStack
            active={surfaceActive()}
            dockRef={() => {}}
            queue={
              <QueuePopover
                items={props.appStore.state.queueDraft?.queue_items ?? []}
                pendingSends={props.appStore.state.pendingSends[props.sessionId] ?? []}
                running={isChatActivityLive(
                  props.appStore,
                  props.sessionId,
                )}
                paused={props.appStore.state.queueDraft?.hold ?? false}
                sending={props.appStore.state.queueDraft?.sending ?? false}
                arranging={queue.arranging()}
                onBeginArrange={() => queue.beginArrange()}
                onEndArrange={() => queue.endArrange()}
                onSetPaused={(paused) => queue.setPaused(paused)}
                onFireNow={(id) => queue.fireNow(id)}
                onRemove={(id) => queue.remove(id)}
                onUpdate={(id, text) => queue.update(id, text)}
                onReorder={(ids) => queue.reorder(ids)}
                onLink={(ids) => queue.link(ids)}
                onUnlink={(ids) => queue.unlink(ids)}
                onSend={() => queue.send()}
                onCancelSend={() => queue.cancelSend()}
                sendWaitingOnApproval={sendWaitingOnApproval()}
              />
            }
            launcher={{
              present: showSessionLauncher,
              children: () => (
                <SessionWorkflowLauncher
                  tiles={launcherTiles()}
                  busy={workflow.busy()}
                  armedWorkflowId={workflow.armed()?.workflow_id}
                  heading={(() => {
                    const armed = workflow.armed();
                    return armed
                      ? `${armed.label} selected — send a message to start`
                      : undefined;
                  })()}
                  onSelect={(wf) => workflow.armWorkflow(wf)}
                  onBrowseAll={() => workflow.toggleDrawerWithPicker()}
                />
              ),
            }}
            arm={{
              present: () => workflow.armed() != null,
              children: () => (
                <Show when={workflow.armed()} keyed>
                  {(armed) => (
                    <div
                      class="den-workflow-arm-chip"
                      data-testid="workflow-arm-chip"
                      role="status"
                    >
                      <span class="den-workflow-arm-chip__label">
                        {armed.label} — send to start
                      </span>
                      <button
                        type="button"
                        class="den-workflow-arm-chip__clear"
                        aria-label={`Clear ${armed.label}`}
                        data-testid="workflow-arm-chip-clear"
                        onClick={() => workflow.clearArmed()}
                      >
                        ×
                      </button>
                    </div>
                  )}
                </Show>
              ),
            }}
            checkpoint={{
              // The slot survives transient reconnects.
              present: () =>
                Boolean(
                  props.sessionId &&
                    pendingCheckpointsForSession(props.appStore, props.sessionId)
                      .length,
                ),
              children: () => (
                <Show when={connectedClient()} keyed>
                  {(client) => (
                    <CheckpointCards
                      appStore={props.appStore}
                      projects={props.projects}
                      client={client}
                      sessionId={props.sessionId}
                      projectDir={props.projectDir}
                    />
                  )}
                </Show>
              ),
            }}
            ask={{
              present: () => Boolean(pendingAsk() && props.sessionId),
              children: () => (
                // Phase changes reset local answer state.
                <Show when={askDockPhaseId()} keyed>
                  {(_phaseId) => (
                    <Show when={retainedPendingAskMeta()} keyed>
                      {(dockMeta) => (
                        <AskUserDock
                          meta={dockMeta}
                          client={clientOrThrow()}
                          sessionId={props.sessionId}
                          runId={pendingAskRun()?.id ?? ""}
                          runRevision={pendingAskRun()?.revision ?? 0}
                          entryKey={pendingAskEntry()?.entryKey}
                          appStore={props.appStore}
                          registerSubmit={(fn) => {
                            askDockSubmit = fn;
                          }}
                          onResolved={() => {
                            viewport.resumeFollowing();
                          }}
                          onAnswerReadyChange={setAskAnswerReady}
                          minimized={askMinimized()}
                          onMinimizedChange={setAskMinimized}
                        />
                      )}
                    </Show>
                  )}
                </Show>
              ),
            }}
            composer={
              <Composer
                approvalsRevision={props.appStore.state.approvalsRevision}
                sidecarStatus={props.appStore.state.sidecarStatus}
                sessionId={props.sessionId}
                projectId={props.projectId}
                claimFocus={surfaceActive()}
                chatHydrationLock={props.appStore.state.chatHydrationLock}
                sessionStatus={props.appStore.state.currentSession?.status}
                focusWhen={props.sessionId}
                refocusPulse={
                  chrome.tabRefocus() +
                  composerRefocus() +
                  askRefocusPulse() +
                  composerAttachFocusPulse()
                }
                rehydrateDraftPulse={composerRehydrate()}
                needsProvider={props.needsProvider}
                activeWorkflow={catalogRun()}
                workflowCatalog={props.appStore.state.workflowCatalog}
                pauseHint={pauseHint()}
                visionSupport={props.visionSupport}
                placeholder={(() => {
                  const armed = workflow.armed();
                  return (
                    (approvalRedirectCheckpoint()
                      ? APPROVALS_COPY.card.composerRedirect.placeholder
                      : null) ??
                    (armed ? armedComposerPlaceholder(armed) : null) ??
                    blueprint.composerPlaceholder() ??
                    workflowFeedbackComposerPlaceholder(
                      activeRun(),
                      pendingAskMeta(),
                    ) ??
                    activeRun()?.ui?.request?.question
                  );
                })()}
                emptySubmitEnabled={
                  Boolean(workflow.armed()) ||
                  activeRun()?.ui?.request?.cadence === "each_turn" ||
                  activeRun()?.ui?.request_state?.status === "waiting"
                }
                streaming={chatLive()}
                pendingTranscriptSend={props.appStore.state.pendingSends[props.sessionId]?.some(send => send.kind === "prompt")}
                stopping={stopping()}
                askPending={Boolean(pendingAsk())}
                askAnswerReady={askAnswerReady()}
                approvalRedirect={Boolean(approvalRedirectCheckpoint())}
                activityLabel={stopping() ? "Stopping…" : thinkingLabel()}
                turnClock={props.appStore.state.turnClock}
                externalContent={sessionHoldsExternalContent()}
                onSend={sendWithStreamFollow}
                onStop={props.onStop}
              />
            }
          />
  ) };
}
