import { startDocumentOutboxRelay } from "./files/documents/document-outbox-relay.ts";
import { documentResidency, windowDocumentBudget } from "./files/documents/document-residency.ts";
import { itemWindowViews } from "./platform/windows/item-windows.ts";
import { editorReplica } from "./files/documents/editor-document.ts";
import { applyFilesBufferDocumentStatus } from "./files/documents/files-buffer-state.ts";
import { Show, createEffect, onCleanup, onMount } from "solid-js";
import { Shell } from "./components/shell/Shell.tsx";
import { createCriticalStop } from "./components/CriticalStopStage.tsx";
import { ConfirmDestructiveHost } from "./components/ConfirmDestructiveDialog.tsx";
import { HostFolderDialog } from "./components/HostFolderDialog.tsx";
import { TextEditContextMenuHost } from "./components/TextEditContextMenuHost.tsx";
import {
  bindAppConnection,
  connectAppBackend,
  disconnectAppBackend,
  getLycaonClient,
  onAttentionEvent,
  onAttentionResync,
  onProjectEvent,
  onSessionEvent,
} from "./platform/connection/app-connection.ts";
import { startAppBoot } from "./platform/connection/app-boot.ts";
import { rememberSessionChatFromStore } from "./chat/session/session-chat-cache.ts";
import { flushTranscriptRowHeightsToDisk } from "./chat/transcript/layout/transcript-row-heights-persist.ts";
import { createAppStore } from "./store/app-state.ts";
import { createRecentsStore } from "./store/recents-store.ts";
import { createProjectsStore } from "./store/projects-store.ts";
import { createShellStore } from "./store/shell-store.ts";
import { createSettingsStore } from "./store/settings-store.ts";
import { createCostStore } from "./store/cost-store.ts";
import { createAttentionStore } from "./store/attention-store.ts";
import { createNoticeStore } from "./notices/notice-store.ts";
import { createEntityRetire } from "./lifecycle/entity-retire.ts";
import { createProjectSessionsStore } from "./store/project-sessions-store.ts";
import { mountNotificationService } from "./notifications/mount-notification-service.ts";
import { windowSubject } from "./platform/windows/window-subject.ts";
import { ChatDestinationPicker } from "./components/chatview/ChatDestinationPicker.tsx";
import { startComposerDocumentMirror } from "./chat/composer/composer-document-store.ts";
import { isTauriRuntime } from "./platform/runtime.ts";
import { nativeUpdateState } from "./settings/system/update-state.ts";
import { listenHostEvent } from "./platform/windows/window-channel.ts";
import { mountUpdateNotice } from "./settings/system/mount-update-notice.ts";
import { EngineStartupStage } from "./components/EngineStartupStage.tsx";
import { engineStartupState } from "./platform/connection/engine-startup.ts";

const appStore = createAppStore();
// Notices persist independently of foreground session changes.
const noticeStore = createNoticeStore();
const recentsStore = createRecentsStore();
const projectsStore = createProjectsStore(() => getLycaonClient(), {
  onProjectEvent,
});
const attentionStore = createAttentionStore(() => getLycaonClient(), {
  onAttentionEvent,
});
onAttentionResync(() => void attentionStore.load());
const projectSessionsStore = createProjectSessionsStore(() => getLycaonClient(), {
  onSessionEvent,
});
// A reopened stream refreshes events missed while disconnected.
onAttentionResync(() => void projectSessionsStore.refresh());
const shellStore = createShellStore("offline");
const settingsStore = createSettingsStore();
const costStore = createCostStore();
const entityRetire = createEntityRetire({
  notices: noticeStore,
  attention: attentionStore,
  recents: recentsStore,
});

function App() {
  const connectionBinding = bindAppConnection({
    app: appStore, settings: settingsStore, costs: costStore, notices: noticeStore,
    retire: entityRetire, recents: recentsStore, shell: shellStore, projects: projectsStore,
  });
  onCleanup(connectionBinding.dispose);
  createEffect(() => documentResidency.setBudget(windowDocumentBudget(1 + itemWindowViews().length)));
  onMount(() => {
    const stopDocumentDelivery = startDocumentOutboxRelay(getLycaonClient, id => !!editorReplica(id), document => applyFilesBufferDocumentStatus(document.project_id, document, true));
    const stopComposerDocumentMirror = startComposerDocumentMirror();
    const subject = windowSubject();
    void startAppBoot({
      appStore,
      projects: projectsStore,
      recents: recentsStore,
      shell: shellStore,
      requestedSession: subject
        && subject.kind === "session"
        ? { projectId: subject.projectId, sessionId: subject.sessionId }
        : null,
      requestedProjectId:
        subject?.kind === "context" || subject?.kind === "file"
          ? subject.projectId
          : null,
      attachOnly: subject != null,
    }, connectionBinding.signal);
    // Only the main window delivers system notifications.
    const unmountNotifications = subject
      ? () => {}
      : mountNotificationService({
          appStore,
          waitingCount: () =>
            attentionStore.state.rows.filter((row) => row.class === "needs_you").length,
        });
    const unmountUpdates = subject || !isTauriRuntime()
      ? () => {}
      : mountUpdateNotice({ notices: noticeStore });
    let stopped = false;
    let stopUpdateRecovery = () => {};
    if (!subject && isTauriRuntime()) {
      void listenHostEvent("update-resume-engine", () => {
        disconnectAppBackend();
        void connectAppBackend(appStore).catch(() => undefined);
      }).then((stop) => {
        if (stopped) stop();
        else stopUpdateRecovery = stop;
      });
    }
    onCleanup(() => {
      stopped = true;
      stopUpdateRecovery();
      stopDocumentDelivery();
      stopComposerDocumentMirror();
      unmountNotifications();
      unmountUpdates();
      rememberSessionChatFromStore(appStore);
      void flushTranscriptRowHeightsToDisk().catch(() => undefined);
      disconnectAppBackend();
    });
  });

  const criticalStop = createCriticalStop(appStore);

  return (
    <>
      {/* Managed startup and critical stops replace Shell while context actions remain available. */}
      <Show
        when={engineStartupState().status === "idle" && !nativeUpdateState.state()?.startup_pending}
        fallback={<EngineStartupStage />}
      >
        <Show when={!criticalStop.spec()} fallback={criticalStop.view()}>
          <Shell
            shell={shellStore}
            recents={recentsStore}
            projects={projectsStore}
            attention={attentionStore}
            projectSessions={projectSessionsStore}
            appStore={appStore}
            notices={noticeStore}
            settingsStore={settingsStore}
            costStore={costStore}
          />
          <ChatDestinationPicker
            projectSessions={projectSessionsStore}
            recents={recentsStore}
          />
        </Show>
      </Show>
      <ConfirmDestructiveHost />
      <HostFolderDialog />
      <TextEditContextMenuHost />
    </>
  );
}

export default App;
