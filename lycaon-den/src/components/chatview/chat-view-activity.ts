import { createMemo, createSignal, onCleanup, onMount } from "solid-js";
import { softPauseWorkerHint } from "../../chat/composer/composer-rules.ts";
import { subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { isPromptStarting, isChatActivityLive, llmTurnFor, sessionActivityFor } from "../../chat/session/session-activity.ts";
import type { Message } from "../../api/types.ts";
import {
  findCurrentUserTurnStart,
  resolveThinkingActivityLabel,
} from "../../chat/session/thinking-activity-label.ts";
import { createThinkingLabelHold } from "../../chat/session/thinking-label-hold.ts";
import { sameSessionWorkers, sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { plainMessageRows } from "../../chat/transcript/projection/plain-message-rows.ts";
import { buildChatTranscriptBlocks } from "../../chat/workflow/workflow-spans.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { projectIdForPath } from "../../store/app-state.ts";
import { sessionLauncherTiles, shouldShowSessionLauncher } from "../../workflow/session-launcher-model.ts";
import { contiguousWindowRows } from "../../chat/transcript/layout/transcript-window.ts";
import type { ChatViewProps } from "./chat-view-props.ts";

export function createChatViewActivity(props: ChatViewProps) {
  const [backgroundProcessRevision, setBackgroundProcessRevision] =
    createSignal(0);

  onMount(() => {
    onCleanup(
      subscribeBackgroundProcessStore(() => {
        setBackgroundProcessRevision((n) => n + 1);
      }, () => ({ sessionId: props.sessionId, statusOnly: true })),
    );
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

  let prevTurnSnapshot: Message[] | undefined;
  let prevFullSnapshot: Message[] | undefined;
  const composerMessages = createMemo<readonly Message[]>(() => {
    const raw = props.appStore.state.messages;
    const len = raw.length;
    const transcript = props.appStore.state.transcript;
    const activities = Object.values(
      sessionActivityFor(props.appStore, props.sessionId).activities ?? {},
    );
    if (len === 0) {
      prevTurnSnapshot = undefined;
      if (prevFullSnapshot?.length === 0) return prevFullSnapshot;
      prevFullSnapshot = [];
      return prevFullSnapshot;
    }
    if (!transcript.hasTailGap) {
      const turnStart = findCurrentUserTurnStart(raw);
      if (turnStart >= 0) {
        const turnSlice = raw.slice(turnStart);
        const needsOlderHistory = activities.some((act) => {
          if (act.status !== "active") return false;
          const callId = act.tool_call_id?.trim();
          const toolName = act.tool_name?.trim().toLowerCase();
          if (!callId || !toolName) return false;
          return !turnSlice.some(
            (msg) =>
              msg.role === "assistant" &&
              msg.tool_calls?.some(
                (c) => c.id.trim() === callId && c.name.trim().toLowerCase() === toolName,
              ),
          );
        });
        if (!needsOlderHistory) {
          prevFullSnapshot = undefined;
          if (
            prevTurnSnapshot &&
            prevTurnSnapshot.length === turnSlice.length &&
            prevTurnSnapshot.every((m, idx) => m === turnSlice[idx])
          ) {
            return prevTurnSnapshot;
          }
          prevTurnSnapshot = turnSlice;
          return turnSlice;
        }
      }
    }
    prevTurnSnapshot = undefined;
    const fullSlice = raw.slice();
    if (
      prevFullSnapshot &&
      prevFullSnapshot.length === fullSlice.length &&
      prevFullSnapshot.every((m, idx) => m === fullSlice[idx])
    ) {
      return prevFullSnapshot;
    }
    prevFullSnapshot = fullSlice;
    return fullSlice;
  });

  const resolvedThinkingLabel = () => {
    backgroundProcessRevision();
    const sessionActivity = sessionActivityFor(props.appStore, props.sessionId);
    return resolveThinkingActivityLabel({
      sessionId: props.sessionId,
      messages: composerMessages(),
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

  const activeRun = () => props.appStore.state.activeWorkflowRun;
  const catalogRun = () =>
    catalogWorkflowRun(
      activeRun(),
      props.appStore.state.workflowRuns,
    );

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
        sessionId: props.sessionId,
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

  const sessionHoldsExternalContent = createMemo(
    () =>
      props.appStore.state.currentSession?.id === props.sessionId &&
      props.appStore.state.currentSession?.untrusted_content === true,
  );

  return { offline, workers, chatLive, stopping, thinkingLabel, activeRun, catalogRun, presentedMessages, spanBlocks, pauseHint, currentProject, launcherTiles, showSessionLauncher, sessionHoldsExternalContent };
}
