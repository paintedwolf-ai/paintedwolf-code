import { createChatGitBinding } from "./chat-git-binding.ts";
import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { createTranscriptViewportController, registerTranscriptViewport, TranscriptViewportProvider } from "../../chat/stream/transcript-viewport.tsx";
import { getLycaonClient, noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";
import { ensureSessionChrome } from "../../chat/session/session-chrome.ts";
import { WorklogPanel } from "../worklog/WorklogPanel.tsx";
import { BlueprintReviewModal } from "../blueprint/BlueprintReviewModal.tsx";
import { SessionWorkersDrawer } from "../worker/SessionWorkersDrawer.tsx";
import { useChatTabChrome } from "../../chat/composer/chat-tab-chrome.tsx";
import { isPendingSessionId } from "../../chat/session/session-scope.ts";
import { setChatTabRailBindings } from "../../chat/composer/chat-tab-rail-bindings.ts";
import { createChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import { createChatTabsState } from "./chat-tabs.ts";
import { createChatWorkflowState } from "./chat-workflow-state.ts";
import { createQueueController } from "./queue-controller.ts";
import { consumeOpenWorklog } from "../../search/search-nav.ts";
import { createWorklogPanelState } from "../worklog/worklog-panel-state.ts";
import { SessionRecoverDialog } from "../transcript/SessionRecoverDialog.tsx";
import { createInvocationLiveToolRecording } from "../../chat/visual/invocation-live-tool-recording.ts";
import type { ChatViewProps } from "./chat-view-props.ts";
import { createChatViewActivity } from "./chat-view-activity.ts";
import { createChatViewRecovery } from "./chat-view-recovery.ts";
import { createChatViewAttachments } from "./chat-view-attachments.ts";
import { createChatViewComposer } from "./chat-view-composer.tsx";
import { ChatViewTranscript } from "./chat-view-transcript.tsx";
import { createChatViewTranscript } from "./chat-view-transcript-runtime.ts";


export function ChatView(props: ChatViewProps) {
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

  const activity = createChatViewActivity(props);
  const { offline, workers, chatLive, board, catalogRun, currentProject } = activity;

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

  const [chatRootEl, setChatRootEl] = createSignal<HTMLElement | undefined>();
  const surfaceActive = () => props.surfaceActive !== false;
  const transcript = createChatViewTranscript(props, viewport, chrome, activity, surfaceActive, () => composer.resetAsk());
  const { scrollToRun } = transcript;

  const [composerRefocus, setComposerRefocus] = createSignal(0);
  const { dragActive, dropBlockedReason } = createChatViewAttachments(props, chatRootEl, catalogRun);
  const [composerRehydrate, setComposerRehydrate] = createSignal(0);
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

  const composer = createChatViewComposer(props, { activity, viewport, chrome, workflow, blueprint, queue, surfaceActive, composerRefocus, composerRehydrate, clientOrThrow, connectedClient, chatNotices });
  const { sendWithStreamFollow } = composer;

  const recoveryState = createChatViewRecovery(props, { chatLive, sendWithStreamFollow, setComposerRehydrate, setComposerRefocus });
  const { recoverTarget, setRecoverTarget, recoverBusy, recoverPreview, recoverPreviewLoading, setRecoverPreviewRevision, recoverError, setRecoverError, confirmRecovery } = recoveryState;

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
          <ChatViewTranscript chat={props} activity={activity} viewport={viewport}
            runtime={transcript} workflow={workflow} blueprint={blueprint}
            recoveryState={recoveryState} sendWithStreamFollow={sendWithStreamFollow}
            connectedClient={connectedClient} chatNotices={chatNotices} surfaceActive={surfaceActive} />
          {composer.render()}
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
