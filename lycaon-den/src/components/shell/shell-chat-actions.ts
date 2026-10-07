import type { LycaonClient } from "../../api/client.ts";
import { beginSessionSnapshotRead } from "../../api/session-snapshot-refresh.ts";
import { clearUnreadBoundary } from "../../attention/unread-boundary.ts";
import { transcriptViewportForSession } from "../../chat/stream/transcript-viewport.tsx";
import { resumeAfterSpendCeiling } from "../../chat/recovery/spend-ceiling-recovery.ts";
import { sendChatPrompt, stopChatActivity } from "../../chat/session/session-lifecycle.ts";
import { shellSessionSwitchGeneration } from "../../chat/session/session-switch.ts";
import { connectAppBackend, getLycaonClient, subscribeProjectEvents } from "../../platform/connection/app-connection.ts";
import { flushEditorDocumentOperations } from "../../files/documents/editor-document.ts";
import type { ActiveChat } from "../../shell/stage-scope.ts";
import type { ShellScope } from "./shell-scope.ts";

type ChatActionDependencies = Pick<ShellScope, "appStore" | "recents" | "projects" | "readyChat" | "activeChat" | "reportError"> & {
  projectDirForChat: () => string;
};

export type ShellChatActions = ReturnType<typeof createShellChatActions>;

export function createShellChatActions({ appStore, recents, projects, readyChat, activeChat,
  projectDirForChat, reportError }: ChatActionDependencies) {
  const sendPrompt = async (
    payload: import("../chatview/Composer.tsx").ComposerSendPayload,
  ): Promise<boolean | void> => {
    const chat = readyChat();
    const text = payload.text?.trim() ?? "";
    const attachments = payload.attachments ?? [];
    const references = payload.references ?? [];
    if (
      !text &&
      attachments.length === 0 &&
      references.length === 0
    ) {
      return false;
    }
    if (!chat) {
      // The draft remains until its destination opens.
      reportError(
        new Error("Still opening this chat — wait a moment and send again."),
      );
      return false;
    }
    const projectDir = projectDirForChat();
    const navigationSignal = shellSessionSwitchGeneration.signal();
    let preparedBackend: ReturnType<typeof beginSessionSnapshotRead> | undefined;
    const shouldSend = (client: LycaonClient) =>
      !navigationSignal.aborted && getLycaonClient() === client &&
      (preparedBackend?.isSameBackend() ?? true);
    // Preparation starts after the pending submission appears.
    const prepareSend = async () => {
      const client = getLycaonClient() ?? (await connectAppBackend(appStore));
      if (!shouldSend(client)) throw new DOMException("Chat connection changed.", "AbortError");
      subscribeProjectEvents(appStore, chat.projectId);
      preparedBackend = beginSessionSnapshotRead(appStore, chat.sessionId);
      // Pending keystrokes become durable before the agent reads documents.
      try {
        await flushEditorDocumentOperations();
      } catch {
        /* Failed editor writes remain queued; prompt submission continues. */
      } finally {
        preparedBackend.finish();
      }
      return client;
    };
    try {
      await sendChatPrompt(
        appStore,
        recents,
        prepareSend,
        chat.sessionId,
        chat.projectId,
        projectDir,
        projects.state.projects,
        text,
        attachments,
        references,
        payload.secrets,
        {
          onPendingSend: payload.onPendingSend,
          recovery: payload.recovery,
          attachmentLabels: payload.attachmentLabels,
          shouldSend,
        },
      );
    } catch {
      /* sendChatPrompt reports the notice */
      return false;
    }
  };

  const resumeSpendLimitedChat = async (chat: ActiveChat) => {
    if (readyChat()?.sessionId !== chat.sessionId) return false;
    return resumeAfterSpendCeiling(appStore, chat.sessionId, (text) => sendPrompt({
      text,
      onPendingSend: () => {
        clearUnreadBoundary(chat.sessionId);
        transcriptViewportForSession(chat.sessionId)?.jumpToTail(true);
      },
    }));
  };

  const stopChat = async () => {
    const chat = activeChat();
    if (!chat) return;
    await stopChatActivity(
      appStore,
      getLycaonClient(),
      chat.sessionId,
      chat.projectId,
      projectDirForChat(),
      projects.state.projects,
    );
  };

  return { sendPrompt, resumeSpendLimitedChat, stopChat };
}
