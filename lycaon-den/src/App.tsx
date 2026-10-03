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
  disconnectAppBackend,
  getLycaonClient,
  onAttentionEvent,
  onAttentionResync,
  onProjectEvent,
  onSessionEvent,
  registerAppStore,
  registerCostStore,
  registerProjectsStore,
  registerRecentsStore,
  registerSettingsStore,
  registerNoticeStore,
  registerEntityRetire,
  registerShellStore,
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
import { EngineStartupStage } from "./components/EngineStartupStage.tsx";
import { engineStartupState } from "./platform/connection/engine-startup.ts";

const appStore = createAppStore();
// Its own store, not a slice of appStore: notices are not
// foreground state. See notices/notice-store.ts.
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
// The stream (re)opening is the moment a client exists and events may have
// been missed — same resync rule the attention store follows.
onAttentionResync(() => void projectSessionsStore.refresh());
const shellStore = createShellStore("offline");
const settingsStore = createSettingsStore();
const costStore = createCostStore();
registerSettingsStore(settingsStore);
registerCostStore(costStore);
registerAppStore(appStore);
registerNoticeStore(noticeStore);
registerEntityRetire(
  createEntityRetire({
    notices: noticeStore,
    attention: attentionStore,
    recents: recentsStore,
  }),
);
registerRecentsStore(recentsStore);
registerShellStore(shellStore);
registerProjectsStore(projectsStore);

function App() {
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
    });
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
      : nativeUpdateState.mount();
    onCleanup(() => {
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
        when={engineStartupState().status === "idle"}
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
