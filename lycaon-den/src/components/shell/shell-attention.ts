import { type ShellScope } from "./shell-scope.ts";
import { createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import { withoutReadFinish } from "../../attention/attention-model.ts";
import { createSeenMarker } from "../../attention/seen-marker.ts";
import { captureUnreadBoundary } from "../../attention/unread-boundary.ts";
import { buildMruEntries, createMruTracker } from "../../chat/session/mru-switcher-model.ts";
import { clearNotificationsOnRead } from "../../notifications/clear-on-read.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { windowFocused } from "../../platform/windows/window-chrome.ts";
import { type ActiveChat } from "../../shell/stage-scope.ts";
import { type AttentionStore } from "../../store/attention-store.ts";

type Dependencies = Pick<ShellScope, "shell" | "appStore" | "projects" | "recents"> & {
  attention: AttentionStore;
  presentedChat: () => ActiveChat | null;
  workspaceMounted: () => boolean;
  conversationShown: () => boolean;
};

export function createShellAttention(props: Dependencies) {
  const { presentedChat, workspaceMounted, conversationShown } = props;
  // Layout effects populate this after the attention memos initialize.
  const [readableSession, setReadableSession] = createSignal<string | null>(null);

  /** Attention scoped to this window. */
  const attentionIndex = () =>
    withoutReadFinish(props.attention.index(), readableSession());

  /** MRU visits include chats opened only for reading. */
  const mru = createMruTracker();

  createEffect(() => {
    const fg = props.shell.state.foreground;
    if (fg) mru.visit(fg.projectId, fg.sessionId);
  });

  const recentChatEntries = createMemo(() => {
    const nameById = new Map(
      props.projects.state.projects.map(
        (p) => [p.id, p.name?.trim() || undefined] as const,
      ),
    );
    const attention = attentionIndex();
    const recents = [...props.recents.state.recents].sort(
      (a, b) => (b.lastActivityAt ?? 0) - (a.lastActivityAt ?? 0),
    );
    return buildMruEntries(mru, recents, {
      projectName: (projectId) => nameById.get(projectId),
      attention: (sessionId) => attention.get(sessionId),
    });
  });

  // Seen stamps require a presented conversation in a focused window.
  createEffect(() => {
    const chat = presentedChat();
    const readable =
      chat != null &&
      workspaceMounted() &&
      conversationShown() &&
      windowFocused();
    setReadableSession(readable && chat ? chat.sessionId : null);
  });

  onCleanup(
    createSeenMarker({
      readable: readableSession,
      // Status and transcript changes refresh the visible chat's seen stamp.
      revision: () =>
        `${props.appStore.state.currentSession?.status ?? ""}:${props.appStore.state.messages.length}`,
      mark: clearNotificationsOnRead(async (sessionId) => {
        const session = await getLycaonClient()?.markSessionSeen(sessionId);
        if (session) props.appStore.actions.noteSessionSeen(session);
      }),
      // Capture the previous seen stamp before this visit's stamp posts.
      onReadable: (sessionId) => {
        const current = props.appStore.state.currentSession;
        captureUnreadBoundary(
          sessionId,
          current?.id === sessionId ? current.seen_at : undefined,
          Date.now(),
        );
      },
    }),
  );
  return { attentionIndex, recentChatEntries };
}
