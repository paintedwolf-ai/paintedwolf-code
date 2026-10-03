import { createChatGitBinding } from "./chat-git-binding.ts";
import type { RewindPreviewResponse } from "../../api/types.ts";
import { watchTranscriptRuntime } from "../../chat/stream/transcript-runtime.ts";
import { Show, createEffect, createMemo, createSignal, on, onCleanup, onMount, untrack } from "solid-js";
import type { CoordinatorVisionSupport } from "../../chat/composer/composer-vision.ts";
import { softPauseWorkerHint, composerComposeBlockReason } from "../../chat/composer/composer-rules.ts";
import { focusRegion, isRegionEntry, registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import {
  addToChat,
  composerAttachBlockMessage,
  composerAttachFocusPulse,
  ingestChatDrop,
} from "../../chat/composer/add-to-chat.ts";
import { registerChatAttachmentDrop } from "../../chat/composer/chat-attachment-drag.ts";
import { registerChatDrop } from "../../platform/files/file-drop.ts";
import { subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import {
  attachThemedViewportScrollbar,
  DEN_SCROLLPORT_AXIS_ATTR,
  DEN_SCROLLPORT_CLASS,
  DEN_SCROLLPORT_CONTENT_CLASS,
  DEN_SCROLLPORT_VIEWPORT_CLASS,
} from "../../platform/scrolling/themed-scrollbars.ts";
import {
  isPromptStarting,
  isChatActivityLive,
  llmTurnFor,
  sessionActivityFor,
} from "../../chat/session/session-activity.ts";
import { resolveThinkingActivityLabel } from "../../chat/session/thinking-activity-label.ts";
import { createThinkingLabelHold } from "../../chat/session/thinking-label-hold.ts";
import { pendingCheckpointsForSession, sameSessionWorkers, sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { plainMessageRows } from "../../chat/transcript/projection/plain-message-rows.ts";
import { resolveContentApply, resolveToolApproval } from "../../chat/checkpoint/checkpoint-actions.ts";
import {
  dockVisibleCheckpointId,
  lastFocusedCheckpointId,
  pickRedirectTarget,
} from "../../chat/checkpoint/redirect-target.ts";
import {
  createTranscriptViewportController,
  registerTranscriptViewport,
  TranscriptViewportProvider,
} from "../../chat/stream/transcript-viewport.tsx";
import { buildChatTranscriptBlocks, scrollTargetForRun } from "../../chat/workflow/workflow-spans.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { WorkflowStartProposalCard } from "../workflow/WorkflowStartProposalCard.tsx";
import type { ProviderKindTemplate, ProviderMeta } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { projectIdForPath } from "../../store/app-state.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import { getLycaonClient, noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { bindPaintInterestLiveStreams } from "../../chat/stream/paint-interest-live.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";
import {
  transcriptDisplayTail,
  transcriptTailDelivery,
  isFirstProseDelivery,
} from "../../chat/transcript/layout/transcript-tail.ts";
import { transcriptViewportForSession } from "../../chat/stream/transcript-viewport.tsx";
import { alignRevealTarget } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { ensureSessionChrome } from "../../chat/session/session-chrome.ts";
import { WorklogPanel } from "../worklog/WorklogPanel.tsx";
import { boardWithWorkerApprovalState } from "../../chat/worker/worker-approval-model.ts";
import { ChatSpanBlocks } from "../transcript/ChatSpanBlocks.tsx";
import type { TranscriptTailSlot } from "../transcript/SessionTranscript.tsx";
import type { MessageRecoveryHandlers } from "../transcript/UserBubble.tsx";
import { QueuePopover } from "./QueuePopover.tsx";
import { Composer } from "./Composer.tsx";
import { ComposerChromeStack } from "./ComposerChromeStack.tsx";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { SessionWorkflowLauncher } from "./SessionWorkflowLauncher.tsx";
import { sessionLauncherTiles, shouldShowSessionLauncher } from "../../workflow/session-launcher-model.ts";
import { CheckpointCards } from "../checkpoint/CheckpointCards.tsx";
import { AskUserDock } from "../workflow/AskUserDock.tsx";
import type { AskUserDockSubmitFn } from "../workflow/AskUserDock.tsx";
import {
  askUserDockMeta,
  pendingAskFeedbackEntry,
  pendingWorkflowFeedback,
  retainAskDockMeta,
  workflowFeedbackComposerPlaceholder,
} from "../../workflow/workflow-feedback-model.ts";
import { NetworkActionContext } from "../../chat/network-action-context.ts";
import { ChatDestinationScope, chatDestinationOf } from "../../chat/composer/chat-destination-scope.tsx";
import { BlueprintReviewModal } from "../blueprint/BlueprintReviewModal.tsx";
import { type OpenWorkerOptions, type WorkerDrawerFocus } from "../../chat/worker/workers-model.ts";
import { SessionWorkersDrawer } from "../worker/SessionWorkersDrawer.tsx";
import { useChatTabChrome } from "../../chat/composer/chat-tab-chrome.tsx";
import { StreamScrollJump } from "../../chat/stream/stream-scroll-jump.tsx";
import { TranscriptDayChip } from "../../chat/stream/transcript-day-chip.tsx";
import { clearUnreadBoundary } from "../../attention/unread-boundary.ts";
import { isPendingSessionId } from "../../chat/session/session-scope.ts";
import { setChatTabRailBindings } from "../../chat/composer/chat-tab-rail-bindings.ts";
import { createChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import { createChatTabsState } from "./chat-tabs.ts";
import { createChatWorkflowState } from "./chat-workflow-state.ts";
import {
  armedComposerPlaceholder,
  armedWorkflowFromPickerRow,
  resolveComposerWorkflowStart,
} from "../../chat/workflow/workflow-arm.ts";
import { clearPendingArmWorkflow, peekArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import { createQueueController } from "./queue-controller.ts";
import { consumeOpenWorklog, openInSearch } from "../../search/search-nav.ts";
import { createWorklogPanelState } from "../worklog/worklog-panel-state.ts";
import { closeFind, findSearchesChat } from "../../find/find-controller.ts";
import { reconcileStreamLayout } from "../../chat/stream/stream-scroll.ts";
import { lastRewindAnchorId, runRewind, type RecoveryTarget } from "../../chat/recovery/session-recovery.ts";
import { registerSessionPromptActions } from "../../notices/notice-actions.ts";
import { SessionRecoverDialog } from "../transcript/SessionRecoverDialog.tsx";
import { hasTurnOutcome, sessionIdleDisposition } from "../../chat/recovery/turn-outcome.ts";
import { TurnOutcomeMarker } from "../transcript/TurnOutcomeMarker.tsx";
import { createInvocationLiveToolRecording } from "../../chat/visual/invocation-live-tool-recording.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { lastTranscriptRow } from "../../chat/transcript/presentation/transcript-keyboard.ts";
import { contiguousWindowRows } from "../../chat/transcript/layout/transcript-window.ts";

/** Poll budget for a run-jump target that mounts on a later virtualizer pass. */
const RUN_JUMP_RETRY_MS = 60;
const RUN_JUMP_RETRIES = 8;

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  appStore: AppStore;
  recents: RecentsStore;
  projects: ProjectsStore;
  projectId: string;
  projectDir: string;
  sessionId: string;
  hasInitialPrompt: boolean;
  /** False while this chat is a hidden resident surface. */
  surfaceActive?: boolean;
  selectedWorkerId?: string | null;
  workersDrawerOpen?: boolean;
  workerDrawerFocus?: WorkerDrawerFocus;
  workersBackgroundHydrate?: boolean;
  onWorkersClose?: () => void;
  onWorkerDrawerFocusHandled?: () => void;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
  onOpenFiles: () => void;
  onSend: (
    payload: import("./Composer.tsx").ComposerSendPayload,
  ) => boolean | void | Promise<boolean | void>;
  onStop: () => void | Promise<void>;
  visionSupport?: CoordinatorVisionSupport;
  needsProvider?: boolean;
  providers?: readonly ProviderMeta[];
  providerKinds?: readonly ProviderKindTemplate[];
};

export function ChatView(props: Props) {
  const chrome = useChatTabChrome();
  const viewport = createTranscriptViewportController({
    sessionId: () => props.sessionId,
  });

  createEffect(() => {
    props.sessionId;
    onCleanup(registerTranscriptViewport(viewport));
  });

  const chatNotices = () =>
    noticeReporterFor(sessionScope(props.projectId, props.sessionId));

  const [backgroundProcessRevision, setBackgroundProcessRevision] =
    createSignal(0);

  onMount(() => {
    onCleanup(
      subscribeBackgroundProcessStore(() => {
        setBackgroundProcessRevision((n) => n + 1);
      }, () => ({ sessionId: props.sessionId, statusOnly: true })),
    );
  });

  createEffect(() => {
    const el = chatRootEl();
    if (!el) return;
    const dropBlockMessage = () => {
      const block = composerComposeBlockReason(
        props.appStore.state.sidecarStatus,
        props.sessionId,
        catalogRun(),
        props.appStore.state.chatHydrationLock,
        props.needsProvider === true,
        props.appStore.state.currentSession?.status,
      );
      return block == null ? null : composerAttachBlockMessage(block);
    };
    const onDragActive = (active: boolean) => {
      setDragActive(active);
      if (!active) {
        setDropBlockedReason(null);
        return;
      }
      setDropBlockedReason(dropBlockMessage());
    };
    const dropBlocked = () => {
      const message = dropBlockMessage();
      if (message == null) return false;
      setDropBlockedReason(message);
      return true;
    };
    const detachFileDrop = registerChatDrop({
      htmlTarget: el,
      onDragActive,
      onDrop: (event) => {
        setDragActive(false);
        if (dropBlocked()) return;
        void ingestChatDrop(event).then((result) => {
          if (result.refusedReason) {
            setDropBlockedReason(result.refusedReason);
          }
        });
      },
    });
    const detachAttachmentDrop = registerChatAttachmentDrop(el, {
      onDragActive,
      onDrop: (ref) => {
        setDragActive(false);
        if (dropBlocked()) return;
        void addToChat(ref, {
          destination: { projectId: props.projectId, sessionId: props.sessionId },
        }).then((result) => {
          if (!result.ok) setDropBlockedReason(result.reason);
        });
      },
    });
    onCleanup(() => {
      detachFileDrop();
      detachAttachmentDrop();
    });
  });

  const offline = () => !isBackendReachable(props.appStore.state.sidecarStatus);

  const workers = createMemo(
    () => sessionWorkers(props.appStore, props.sessionId),
    undefined,
    { equals: sameSessionWorkers },
  );
  const chatLive = () => isChatActivityLive(props.appStore, props.sessionId);
  const stopping = () =>
    sessionActivityFor(props.appStore, props.sessionId).stopping === true;
  const llmTurn = () => llmTurnFor(props.appStore, props.sessionId);

  const resolvedThinkingLabel = () => {
    backgroundProcessRevision();
    const sessionActivity = sessionActivityFor(props.appStore, props.sessionId);
    return resolveThinkingActivityLabel({
      sessionId: props.sessionId,
      messages: props.appStore.state.messages,
      activities: Object.values(sessionActivity.activities ?? {}),
      coordinatorLoop: props.appStore.state.coordinatorLoopProgress,
      progressSteps: props.appStore.state.progress?.steps,
      progressRunning: props.appStore.state.turnClock?.running,
      batchPhase: props.appStore.state.coordinatorRunContext?.batch_phase,
      activeWorkflowRun: props.appStore.state.activeWorkflowRun,
      workers: workers(),
      llmTurnActivity: llmTurn(),
      promptStarting: isPromptStarting(props.appStore, props.sessionId),
      providers: props.providers,
      providerKinds: props.providerKinds,
    });
  };

  const thinkingLabel = createThinkingLabelHold({
    visible: chatLive,
    resolveLabel: resolvedThinkingLabel,
    immediate: () => Object.values(
      sessionActivityFor(props.appStore, props.sessionId).activities ?? {},
    ).some((activity) => activity.status === "active" && activity.kind === "running_tool" && activity.progress !== undefined),
  });

  const board = () =>
    boardWithWorkerApprovalState(
      props.appStore.state.board,
      workers(),
      props.appStore.state.pendingCheckpoints,
    );
  const activeRun = () => props.appStore.state.activeWorkflowRun;
  const catalogRun = () =>
    catalogWorkflowRun(
      activeRun(),
      props.appStore.state.workflowRuns,
    );

  const tabs = createChatTabsState({
    chrome,
    appStore: props.appStore,
    projects: props.projects.state.projects,
    sessionId: () => props.sessionId,
    projectDir: () => props.projectDir,
    offline,
  });

  const worklog = createWorklogPanelState({
    appStore: props.appStore,
    sessionId: () => props.sessionId,
    offline,
  });

  createInvocationLiveToolRecording({
    sessionId: () => props.sessionId?.trim() ?? "",
    getClient: () => getLycaonClient(),
    isActive: () =>
      Boolean(props.sessionId?.trim()) &&
      !isPendingSessionId(props.sessionId?.trim() ?? ""),
  });

  createEffect(() => {
    if (consumeOpenWorklog()) {
      worklog.open();
    }
  });

  createEffect(() => {
    const sessionId = props.sessionId;
    if (!sessionId || isPendingSessionId(sessionId)) return;
    // Connection changes retry session chrome hydration.
    const connected = isBackendReachable(props.appStore.state.sidecarStatus);
    if (!connected) return;
    const client = getLycaonClient();
    if (!client) return;
    void ensureSessionChrome(props.appStore, client, sessionId).catch(
      () => undefined,
    );
  });

  // Across a tail gap the chat presents only the history that reads contiguously.
  const presentedMessages = createMemo(() => {
    const transcript = props.appStore.state.transcript;
    return plainMessageRows(
      transcript.hasTailGap ? contiguousWindowRows(transcript) : props.appStore.state.messages,
    );
  });
  const spanBlocks = createMemo<ReturnType<typeof buildChatTranscriptBlocks>>((previous) => {
    if (props.appStore.state.currentSession?.id !== props.sessionId) return previous ?? [];
    return buildChatTranscriptBlocks(
      presentedMessages(),
      props.appStore.state.workflowRuns,
      activeRun(),
      {
        verboseMode: verboseModePref(),
        workers: workers(),
        pendingCheckpoints: props.appStore.state.pendingCheckpoints,
        turnLoads: props.appStore.state.turnLoads,
      },
    );
  }, []);
  const pauseHint = () => {
    const sessionId = props.sessionId;
    if (!sessionId) return null;
    return softPauseWorkerHint(catalogRun(), workers());
  };

  const clientOrThrow = () => {
    const client = getLycaonClient();
    if (!client) throw new Error("The app is still starting. Try again in a moment.");
    return client;
  };

  /** Status supplies the reactive connection edge. */
  const connectedClient = () =>
    props.appStore.state.sidecarStatus === "connected"
      ? getLycaonClient()
      : null;

  const [streamEl, setStreamEl] = createSignal<HTMLDivElement>();
  const [chatRootEl, setChatRootEl] = createSignal<HTMLElement | undefined>();
  const chatFocusClaim = {};
  const surfaceActive = () => props.surfaceActive !== false;

  createEffect(() => {
    const el = streamEl();
    if (el && surfaceActive()) {
      el.tabIndex = -1;
      registerFocusRegion("chat", el, chatFocusClaim);
      // Pointer and scripted focus stay on the stream so paging keys scroll it.
      const onFocus = () => {
        if (!isRegionEntry(el)) return;
        if (!focusRegion("composer")) lastTranscriptRow(el)?.focus({ preventScroll: true });
      };
      el.addEventListener("focus", onFocus);
      onCleanup(() => el.removeEventListener("focus", onFocus));
    } else {
      releaseFocusRegion("chat", chatFocusClaim);
    }
  });
  onCleanup(() => releaseFocusRegion("chat", chatFocusClaim));
  const [composerRefocus, setComposerRefocus] = createSignal(0);
  const [askRefocusPulse, setAskRefocusPulse] = createSignal(0);
  const [dragActive, setDragActive] = createSignal(false);
  const [dropBlockedReason, setDropBlockedReason] = createSignal<string | null>(
    null,
  );
  const [recoverTarget, setRecoverTarget] = createSignal<RecoveryTarget | null>(
    null,
  );
  const [recoverError, setRecoverError] = createSignal<string | null>(null);
  const [recoverBusy, setRecoverBusy] = createSignal(false);
  const [recoverPreview, setRecoverPreview] = createSignal<RewindPreviewResponse | null>(null);
  const [recoverPreviewLoading, setRecoverPreviewLoading] = createSignal(false);
  const [recoverPreviewRevision, setRecoverPreviewRevision] = createSignal(0);
  createEffect(() => {
    recoverPreviewRevision();
    const target = recoverTarget();
    const sessionId = props.sessionId;
    setRecoverPreview(null);
    setRecoverError(null);
    if (!target) { setRecoverPreviewLoading(false); return; }
    const client = getLycaonClient();
    if (!client) { setRecoverPreviewLoading(false); setRecoverError("The host is unavailable."); return; }
    let active = true;
    onCleanup(() => { active = false; });
    setRecoverPreviewLoading(true);
    void client.previewSessionRewind(sessionId, { message_id: target.messageId })
      .then((preview) => { if (active) setRecoverPreview(preview); })
      .catch((error: unknown) => { if (active) setRecoverError(error instanceof Error ? error.message : "Could not preview this rewind."); })
      .finally(() => { if (active) setRecoverPreviewLoading(false); });
  });
  const [composerRehydrate, setComposerRehydrate] = createSignal(0);
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

  const scrollToRun = (run: import("../../api/types.ts").WorkflowRun) => {
    const target = scrollTargetForRun(run);
    if (!target || !streamEl()) return;
    const viewport = transcriptViewportForSession(props.sessionId);
    if (!viewport) return;
    void viewport.revealAnchor({
      chicklet: "message",
      anchorId: target,
    }, { align: "start" }).then(() => {
      // Alignment waits for the virtual row to mount.
      const attempt = (left: number): void => {
        const host = streamEl();
        if (!host) return;
        const el = host.querySelector<HTMLElement>(`#msg-${target}`);
        if (el) {
          alignRevealTarget(el);
          return;
        }
        if (left > 0) setTimeout(() => attempt(left - 1), RUN_JUMP_RETRY_MS);
      };
      attempt(RUN_JUMP_RETRIES);
    });
  };

  const workflow = createChatWorkflowState({
    appStore: props.appStore,
    projects: props.projects.state.projects,
    sessionId: () => props.sessionId,
    projectDir: () => props.projectDir,
    catalogRun,
    workflowsOpen: tabs.workflowsOpen,
    setWorkflowsOpen: tabs.setWorkflowsOpen,
    scrollToRun,
    clientOrThrow,
    onArmed: () => setComposerRefocus((n) => n + 1),
  });

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
    const sendResult = await props.onSend({
      ...payload,
      onPendingSend: (destination) => {
        payload.onPendingSend?.(destination);
        if (destination === "transcript") {
          clearUnreadBoundary(props.sessionId);
          viewport.jumpToTail(false);
        }
      },
    });
    if (sendResult === false) return false;
    // Admission confirmation preserves any intervening reader input.
    return sendResult;
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

  const lastAskId = createMemo(() =>
    lastRewindAnchorId(props.appStore.state.messages),
  );

  const turnHasProgress = createMemo(() => {
    const messages = props.appStore.state.messages;
    const anchorId = lastAskId();
    if (!anchorId) return false;
    const anchorIdx = messages.findIndex((m) => m.id === anchorId);
    if (anchorIdx === -1) return false;
    for (let i = anchorIdx + 1; i < messages.length; i++) {
      const m = messages[i];
      if (m && (m.role === "assistant" || m.role === "tool" || (m.tool_calls && m.tool_calls.length > 0))) {
        return true;
      }
    }
    return false;
  });

  // Persistent row actions keep turn-state changes from resizing rows.
  const recovery: MessageRecoveryHandlers = {
    held: chatLive,
    onEdit: (messageId, text) =>
      setRecoverTarget({ action: "edit", messageId, text, operationId: crypto.randomUUID() }),
    onRewind: (messageId, text) =>
      setRecoverTarget({ action: "rewind", messageId, text, operationId: crypto.randomUUID() }),
    onCopy: (text) => void copyTextToClipboard(text),
  };

  const recoveryAction = (action: "continue" | "retry", text: string) => {
    const afterMessageId = props.appStore.state.messages.at(-1)?.id;
    if (!afterMessageId) return undefined;
    return () => sendWithStreamFollow({
      text, recovery: { action, after_message_id: afterMessageId },
    });
  };

  const retryLastAsk = () => {
    const messages = props.appStore.state.messages;
    const id = lastAskId();
    const ask = id ? messages.find((m) => m.id === id) : undefined;
    const text = ask?.content?.trim();
    if (!text || chatLive()) return undefined;
    return recoveryAction("retry", text);
  };

  const keepGoingLastAsk = () => {
    if (chatLive()) return undefined;
    return recoveryAction("continue", "Keep going");
  };

  const rewindAndRetryLastAsk = () => {
    const messages = props.appStore.state.messages;
    const id = lastAskId();
    const ask = id ? messages.find((m) => m.id === id) : undefined;
    const text = ask?.content?.trim();
    const client = getLycaonClient();
    if (!id || !text || !client || chatLive()) return undefined;

    return async () => {
      try {
        const preview = await client.previewSessionRewind(props.sessionId, { message_id: id });
        if (!preview.plan_digest || preview.issues.length > 0) {
          setRecoverTarget({ action: "rewind", messageId: id, text, operationId: crypto.randomUUID() });
          return;
        }
        await runRewind(
          {
            client,
            appStore: props.appStore,
            projectId: props.projectId,
            sessionId: props.sessionId,
            projectDir: props.projectDir,
            projects: props.projects.state.projects,
          },
          { action: "rewind", messageId: id, text, operationId: crypto.randomUUID() },
          preview.plan_digest,
        );
        setComposerRehydrate((n) => n + 1);
        setComposerRefocus((n) => n + 1);
        await sendWithStreamFollow({ text });
      } catch {
        setRecoverTarget({ action: "rewind", messageId: id, text, operationId: crypto.randomUUID() });
      }
    };
  };

  createEffect(
    on(
      () => props.sessionId,
      (sessionId) => {
        if (!sessionId) return;
        onCleanup(
          registerSessionPromptActions(sessionId, {
            retry: () => void retryLastAsk()?.(),
            keepGoing: () => void keepGoingLastAsk()?.(),
            rewindAndRetry: () => void rewindAndRetryLastAsk()?.(),
          }),
        );
      },
    ),
  );

  const confirmRecovery = async () => {
    const target = recoverTarget();
    const client = getLycaonClient();
    const preview = recoverPreview();
    if (!target || !client || !preview?.plan_digest || preview.issues.length > 0) return;
    setRecoverBusy(true);
    setRecoverError(null);
    try {
      await runRewind(
        {
          client,
          appStore: props.appStore,
          projectId: props.projectId,
          sessionId: props.sessionId,
          projectDir: props.projectDir,
          projects: props.projects.state.projects,
        },
        target,
        preview.plan_digest,
      );
      setRecoverTarget(null);
      setComposerRehydrate((n) => n + 1);
      setComposerRefocus((n) => n + 1);
    } catch (err) {
      setRecoverError(
        err instanceof Error ? err.message : "Could not complete that.",
      );
    } finally {
      setRecoverBusy(false);
    }
  };

  const blueprint = createChatBlueprintState({
    appStore: props.appStore,
    projects: props.projects.state.projects,
    sessionId: () => props.sessionId,
    projectDir: () => props.projectDir,
    catalogRun,
    catalog: () => props.appStore.state.workflowCatalog,
    workflowsOpen: tabs.workflowsOpen,
    setWorkflowsOpen: tabs.setWorkflowsOpen,
    withWorkflow: workflow.withWorkflow,
    clientOrThrow,
    onOpenFiles: props.onOpenFiles,
    exitWorkflow: (reason) => workflow.bindExit(reason),
  });

  const queue = createQueueController({
    appStore: props.appStore,
    sessionId: () => props.sessionId,
    // Both queue send actions resume following.
    onSend: () => {
      viewport.jumpToTail(true);
    },
  });

  const currentProject = () => {
    const list = props.projects.state.projects;
    const id =
      props.projectId ||
      projectIdForPath(list, props.projectDir) ||
      props.appStore.state.currentSession?.project_id ||
      "";
    return props.projects.byId(id);
  };
  const launcherTiles = () =>
    sessionLauncherTiles(props.appStore.state.workflowCatalog, {
      hasRepo: (currentProject()?.roots.length ?? 0) > 0,
    });
  // Seeded system messages do not hide the launcher.
  const hasStartedChat = () =>
    props.appStore.state.messages.some((m) => m.role !== "system");
  const showSessionLauncher = () =>
    shouldShowSessionLauncher({
      hasInitialPrompt: props.hasInitialPrompt,
      hasMessages: hasStartedChat(),
      hasCatalogRun: catalogRun() != null,
      streaming: isChatActivityLive(props.appStore, props.sessionId),
      tileCount: launcherTiles().length,
    });

  let prevDisplayTail: ReturnType<typeof transcriptDisplayTail> = null;
  let prevSessionId: string | undefined;

  watchTranscriptRuntime({
    active: surfaceActive,
    sessionId: () => props.sessionId,
    currentSessionId: () => props.appStore.state.currentSession?.id,
    hydrating: () => props.appStore.state.chatHydrationLock != null,
    stream: streamEl,
    bind: () => viewport.bindRuntime({
      client: () => getLycaonClient() ?? undefined,
      appStore: props.appStore,
      sessionId: () => props.sessionId,
      tabOpen: () => chrome.openTab() != null,
      panelRetracted: chrome.panelRetracted,
      panelHeightPx: chrome.panelHeightPx,
      onPanelRetractedChange: chrome.setPanelRetracted,
    }),
  });

  // Find holds chat matches still; closing it hands the tail back to following.
  createEffect(
    on(findSearchesChat, (searching) => {
      const host = streamEl();
      if (!searching && host) reconcileStreamLayout(host);
    }, { defer: true }),
  );

  bindPaintInterestLiveStreams({
    appStore: props.appStore,
    active: surfaceActive,
    client: () => getLycaonClient(),
    sessionId: () => props.sessionId,
    hydrationLock: () => props.appStore.state.chatHydrationLock,
    paintedWorker: () => {
      if (props.workersDrawerOpen !== true) return null;
      const workerId = props.selectedWorkerId?.trim();
      if (!workerId) return null;
      const row = workers().find((w) => w.id === workerId);
      const childSessionId = row?.child_session_id?.trim();
      if (!childSessionId) return null;
      return { workerId, childSessionId };
    },
  });


  createEffect(() => {
    viewport.chromeChanged({
      tabOpen: chrome.openTab() != null,
      panelRetracted: chrome.panelRetracted(),
      panelHeightPx: chrome.panelHeightPx(),
    });
  });

  createEffect(() => {
    props.sessionId;
    chrome.setPanelRetracted(false);
    setAskMinimized(false);
  });

  createEffect(() => {
    const sessionId = props.sessionId;
    if (props.appStore.state.currentSession?.id !== sessionId) return;
    const tail = transcriptDisplayTail(spanBlocks(), prevDisplayTail);
    const hydrating = props.appStore.state.chatHydrationLock != null;

    if (sessionId !== prevSessionId) {
      // A pending session id can resolve without replacing its transcript.
      const inPlaceIdSwap =
        prevSessionId !== undefined &&
        tail?.key === prevDisplayTail?.key &&
        tail?.itemCount === prevDisplayTail?.itemCount;
      const previousSessionId = prevSessionId;
      prevSessionId = sessionId;
      prevDisplayTail = tail;
      const restored = viewport.activateSession(
        sessionId,
        previousSessionId,
        inPlaceIdSwap,
      );
      if (!inPlaceIdSwap && !restored) {
        viewport.primeTail(600);
      }
      return;
    }

    // Resume hydration lands at the bottom without a growth animation.
    if (hydrating) {
      prevDisplayTail = tail;
      if (untrack(viewport.following)) viewport.primeTail(400);
      return;
    }

    const delivery = transcriptTailDelivery(prevDisplayTail, tail);
    const firstContent = isFirstProseDelivery(prevDisplayTail, tail);
    prevDisplayTail = tail;

    if (!delivery) return;

    const tabOpen = chrome.openTab() != null && !chrome.panelRetracted();
    if (tabOpen && delivery === "prose") {
      return;
    }

    if (delivery === "prose") {
      if (tail) viewport.contentChanged({ delivery, rowKey: tail.key, firstContent });
    } else {
      viewport.contentChanged({ delivery });
    }
  });

  const gitBinding = createChatGitBinding({
    appStore: () => props.appStore,
    projects: () => props.projects,
    projectDir: () => props.projectDir,
    projectId: () => props.projectId,
    sessionId: () => props.sessionId,
    clientOrThrow, chatNotices, sendWithStreamFollow,
  });

  // The stream controls terminal worker state.
  const [cancellingWorkerId, setCancellingWorkerId] = createSignal<
    string | null
  >(null);

  const cancelWorker = (workerId: string) => {
    const client = getLycaonClient();
    if (!client || cancellingWorkerId()) return;
    setCancellingWorkerId(workerId);
    void client.cancelWorker(workerId)
      .catch((err) => chatNotices().reportError(err))
      .finally(() => setCancellingWorkerId(null));
  };

  // Bindings belong to the visible resident layer.
  const railBindingsClaim = {};

  createEffect(() => {
    if (!surfaceActive()) {
      setChatTabRailBindings(null, railBindingsClaim);
      return;
    }
    setChatTabRailBindings(
      {
        workflows: {
          workflowPickerOpen: workflow.pickerOpen,
          workflowError: workflow.error,
          workflowBusy: workflow.busy,
          onExit: (run) => workflow.bindExit(undefined, run),
          onPause: () => workflow.bindPause(),
          onResume: () => workflow.bindResume(),
          onAdvance: () => workflow.bindAdvance(),
          onReviewInChat: () => blueprint.scrollToBlueprintCard(),
          onOpenPicker: () => workflow.openPicker(),
          onClosePicker: () => workflow.closePicker(),
          onArmWorkflow: (wf) => workflow.armWorkflow(wf),
          onJumpToRun: (run) => workflow.bindJumpToRun(run),
        },
        git: gitBinding(),
        onOpenWorklog: () => worklog.open(),
        onCloseWorklog: () => worklog.close(),
        worklogOpen: worklog.isOpen,
        onCancelWorker: cancelWorker,
        cancellingWorkerId,
      },
      railBindingsClaim,
    );
  });
  onCleanup(() => setChatTabRailBindings(null, railBindingsClaim));

  // Block writes a global deny and mirrors it optimistically.
  const [blockedHosts, setBlockedHosts] = createSignal<ReadonlySet<string>>(
    new Set(),
  );
  const networkActions = {
    blockHost: async (host: string) => {
      const target = host.trim();
      const client = getLycaonClient();
      if (!target || !client) return;
      try {
        const current = await client.getApprovalsSettings();
        const rules = [
          ...current.rules.filter(
            (r) => !(r.category === "host" && r.pattern === target),
          ),
          {
            category: "host" as const,
            pattern: target,
            effect: "deny" as const,
          },
        ];
        await client.updateApprovalsSettings({ rules });
        setBlockedHosts((prev) => new Set(prev).add(target));
      } catch (err) {
        chatNotices().reportError(err);
      }
    },
    isBlocked: (host: string) => blockedHosts().has(host.trim()),
  };

  const sessionHoldsExternalContent = createMemo(
    () =>
      props.appStore.state.currentSession?.id === props.sessionId &&
      props.appStore.state.currentSession?.untrusted_content === true,
  );

  // Tail slots preserve transcript reading order.
  const transcriptTail: readonly TranscriptTailSlot[] = [
    {
      id: "turn-outcome",
      present: () =>
        !chatLive() &&
        (props.appStore.state.pendingSends[props.sessionId]?.length ?? 0) === 0 &&
        hasTurnOutcome(sessionIdleDisposition(props.sessionId)),
      children: () => (
        <TurnOutcomeMarker
          disposition={sessionIdleDisposition(props.sessionId)}
          hasProgress={turnHasProgress()}
          onRetry={() => void retryLastAsk()?.()}
          onKeepGoing={() => void keepGoingLastAsk()?.()}
          onRewindAndRetry={() => void rewindAndRetryLastAsk()?.()}
        />
      ),
    },
    {
      id: "workflow-start-proposal",
      present: () => workflow.startProposal() != null,
      children: () => (
        <Show when={workflow.startProposal()} keyed>
          {(proposal) => (
            <WorkflowStartProposalCard
              proposal={proposal}
              catalog={props.appStore.state.workflowCatalog}
              busy={workflow.busy()}
              onStart={() => workflow.startProposalWorkflow(proposal)}
              onOpenDrawer={() => workflow.openDrawerWithPicker()}
              onDismiss={() => workflow.dismissProposal()}
            />
          )}
        </Show>
      ),
    },
  ];

  return (
    // Nested surfaces report against this chat.
    <NoticeReporterProvider reporter={chatNotices()}>
    <TranscriptViewportProvider value={viewport}>
      <section
        class="den-chat"
        aria-label="Chat"
        classList={{
          "den-chat--context-drawer-open":
            worklog.isOpen() || props.workersDrawerOpen === true,
        }}
        data-testid="chat-view"
        ref={setChatRootEl}
      >
        <Show when={dragActive()}>
          <div
            class="den-chat-drop-overlay"
            data-testid="chat-drop-overlay"
            aria-hidden="true"
          >
            <p class="den-chat-drop-overlay__affordance">
              {dropBlockedReason() ??
                "Drop to add to chat"}
            </p>
          </div>
        </Show>
        <WorklogPanel
          open={worklog.isOpen()}
          findings={props.appStore.state.findings}
          board={board()}
          workers={workers()}
          messages={props.appStore.state.messages}
          pendingCheckpoints={props.appStore.state.pendingCheckpoints}
          onOpenWorkerCheckpoint={(workerId) => {
            worklog.close();
            props.onOpenWorker(workerId);
          }}
          onClose={() => worklog.close()}
        />
        <SessionWorkersDrawer
          appStore={props.appStore}
          projects={props.projects.state.projects}
          projectDir={props.projectDir}
          sessionId={props.sessionId}
          open={props.workersDrawerOpen === true}
          selectedId={props.selectedWorkerId ?? null}
          scrollToFocus={props.workerDrawerFocus}
          onScrollToFocusHandled={props.onWorkerDrawerFocusHandled}
          backgroundHydrate={props.workersBackgroundHydrate}
          onClose={() => props.onWorkersClose?.()}
        />
        <div class="den-chat-conversation">
          <div class="den-chat-stream-wrap">
            {/* The outer frame keeps scrollbar chrome stationary. */}
            <div
              class={`${DEN_SCROLLPORT_CLASS} den-chat-stream-frame`}
              {...{ [DEN_SCROLLPORT_AXIS_ATTR]: "y" }}
              data-session-id={props.sessionId}
            >
            <div
              class={`${DEN_SCROLLPORT_VIEWPORT_CLASS} den-chat-stream`}
              ref={(el) => {
                let detachScrollbar: (() => void) | undefined;
                queueMicrotask(() => {
                  const frame = el.parentElement;
                  if (!el.isConnected || !(frame instanceof HTMLDivElement)) return;
                  const body = el.querySelector<HTMLElement>(".den-chat-stream-body");
                  detachScrollbar = attachThemedViewportScrollbar(frame, el, {
                    axis: "y",
                    ...(body ? { extent: body } : {}),
                  });
                  // Stream publication follows motion-controller attachment.
                  viewport.attachStream(el);
                  setStreamEl(el);
                });
                onCleanup(() => {
                  if (streamEl() === el) setStreamEl(undefined);
                  if (viewport.stream() === el) viewport.attachStream(null);
                  detachScrollbar?.();
                });
              }}
              data-testid="chat-stream"
              data-session-id={props.sessionId}
            >
              <div class={`${DEN_SCROLLPORT_CONTENT_CLASS} den-chat-stream-body`}>
                <NetworkActionContext.Provider value={networkActions}>
                  <ChatDestinationScope destination={chatDestinationOf(props.projectId, props.sessionId)}>
                    <ChatSpanBlocks
                      blocks={spanBlocks()}
                      messages={presentedMessages()}
                      sessionId={props.sessionId}
                      surfaceActive={surfaceActive}
                      visibleTurnActive={chatLive()}
                      workers={workers()}
                      workerTranscripts={
                        props.appStore.state.workerTranscripts
                      }
                      onOpenWorker={(id, opts) =>
                        props.onOpenWorker(id, opts)
                      }
                      checkpointClient={connectedClient()}
                      checkpointAppStore={props.appStore}
                      projectDir={props.projectDir}
                      projectId={props.projectId}
                      rootRefs={
                        (
                          props.projects.byId(props.projectId) ??
                          props.projects.state.projects.find(
                            (p) => p.id === props.projectId,
                          )
                        )?.roots ?? []
                      }
                      onExploreInSearch={(query) => {
                        void openInSearch(props.projectId, query);
                      }}
                      blueprint={blueprint}
                      onBlueprintRevise={(blueprintPath, text) => {
                        blueprint.bindBlueprintPath(blueprintPath);
                        void sendWithStreamFollow({ text });
                      }}
                      activePendingPhaseId={
                        pendingWorkflowFeedback(activeRun())?.phase_id ?? null
                      }
                      recovery={recovery}
                      pendingSends={viewport.presentsLiveTail() ? props.appStore.state.pendingSends[props.sessionId] ?? [] : []}
                      turnClocks={props.appStore.state.turnClocks}
                      turnLoads={props.appStore.state.turnLoads}
                      tail={viewport.presentsLiveTail() ? transcriptTail : []}
                    />
                  </ChatDestinationScope>
                </NetworkActionContext.Provider>
              </div>
            </div>
            </div>
            <div
              class="den-chat-stream-edge den-chat-stream-edge--top"
              aria-hidden="true"
            />
            <div
              class="den-chat-stream-edge den-chat-stream-edge--bottom"
              aria-hidden="true"
            />
            <TranscriptDayChip />
            <StreamScrollJump />
          </div>
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
        </div>

        <SessionRecoverDialog
          target={recoverTarget()}
          busy={recoverBusy()}
          stopsLive={chatLive()}
          preview={recoverPreview()}
          roots={currentProject()?.roots}
          previewLoading={recoverPreviewLoading()}
          onRefresh={() => setRecoverPreviewRevision((n) => n + 1)}
          error={recoverError()}
          onConfirm={() => void confirmRecovery()}
          onCancel={() => {
            setRecoverTarget(null);
            setRecoverError(null);
          }}
        />

        <BlueprintReviewModal
          reviewOpen={blueprint.reviewOpen()}
          initialView={blueprint.reviewView()}
          selectedBlueprintPath={blueprint.selectedBlueprintPath()}
          blueprintName={blueprint.name()}
          error={blueprint.error()}
          warning={blueprint.warning()}
          projectId={props.projectId}
          appStore={props.appStore}
          roots={currentProject()?.roots ?? []}
          awaitingApproval={blueprint.awaitingApproval()}
          canApprove={blueprint.canApprove()}
          changed={blueprint.blueprintChangedWarning()}
          choiceTransitions={blueprint.choiceTransitions()}
          onClose={() => blueprint.closeReview()}
          onOpenInFiles={() => blueprint.openInFiles()}
          onDocumentSessionChange={blueprint.setDocumentSession}
          onApprove={() => blueprint.approve()}
          onChoiceTransition={(id) => blueprint.fireChoiceTransition(id)}
          onRequestChanges={async (text) => {
            if (!(await blueprint.savePendingEdits())) return false;
            const blueprintPath = blueprint.activeBlueprintPath();
            if (blueprintPath) blueprint.bindBlueprintPath(blueprintPath);
            blueprint.closeReview();
            await sendWithStreamFollow({ text });
            return true;
          }}
        />
      </section>
    </TranscriptViewportProvider>
    </NoticeReporterProvider>
  );
}
