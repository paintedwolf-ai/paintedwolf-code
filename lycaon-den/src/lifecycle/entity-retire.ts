import { clearComposerDrafts } from "../chat/composer/composer-drafts.ts";
import { dropFilesBuffersForProject } from "../files/documents/project-files-buffers.ts";
import { clearDraftVersionCache } from "../chat/draft/draft-version-cache.ts";
import {
  dropSessionChatCacheForProject,
  dropSessionChatCacheForSession,
} from "../chat/session/session-chat-cache.ts";
import { forgetPersistedLastSessionSnapshot } from "../chat/session/session-chat-persist.ts";
import type { SessionChatScope } from "../chat/session/session-chat-snapshot.ts";
import { forgetTranscriptViewport } from "../chat/stream/transcript-viewport-state.ts";
import { dropPersistedTranscriptRowHeights } from "../chat/transcript/layout/transcript-row-heights-persist.ts";
import { cancelNotificationsForSessions } from "../platform/desktop/notifications.ts";
import { forgetBackgroundProcessSession } from "../chat/tool/background-process-store.ts";
import { clearTranscriptEntryMemory } from "../chat/transcript/presentation/transcript-entry.ts";
import { forgetInvocationRecordingSession } from "../chat/visual/invocation-recording-store.ts";
import { forgetLivePreviewSession } from "../chat/visual/preview-store.ts";
import type { NoticeStore } from "../notices/notice-store.ts";
import type { AttentionStore } from "../store/attention-store.ts";
import type { RecentsStore } from "../store/recents-store.ts";
import { endSourceViewsForChat } from "../ui/paged-view/source-view-session.ts";

export type EntityRetire = {
  /** The one retirement for a chat the host no longer has, however Den learned it. */
  session: (scope: SessionChatScope) => void;
  project: (projectId: string) => void;
};

export type EntityRetireDeps = {
  notices: NoticeStore;
  attention: AttentionStore;
  recents: RecentsStore;
};

/** Retires Den projections keyed by a deleted session or project. Navigation
 *  away from a retired chat belongs to the caller. */
export function createEntityRetire(deps: EntityRetireDeps): EntityRetire {
  const retireChat = (scope: SessionChatScope) => {
    retireSessionMemory(scope.sessionId);
    retireSessionState(scope);
    void deps.recents.removeRecent(scope).catch(() => undefined);
  };
  return {
    session(scope) {
      const target = { projectId: scope.projectId.trim(), sessionId: scope.sessionId.trim() };
      if (!target.sessionId) return;
      deps.notices.clearSession(target.sessionId);
      deps.attention.dropSession(target.sessionId);
      dropSessionChatCacheForSession(target.sessionId);
      retireChat(target);
      void cancelNotificationsForSessions([target.sessionId]);
    },
    project(projectId) {
      const id = projectId.trim();
      if (!id) return;
      const sessionIds = sessionIdsForProject(deps, id);
      deps.notices.clearProject(id);
      deps.attention.dropProject(id);
      dropFilesBuffersForProject(id);
      dropSessionChatCacheForProject(id);
      for (const sessionId of sessionIds) retireChat({ projectId: id, sessionId });
      void cancelNotificationsForSessions(sessionIds);
    },
  };
}

function retireSessionMemory(sessionId: string): void {
  clearComposerDrafts([sessionId]);
  clearDraftVersionCache(sessionId);

  clearTranscriptEntryMemory(sessionId);
  forgetBackgroundProcessSession(sessionId);
  forgetInvocationRecordingSession(sessionId);
  forgetLivePreviewSession(sessionId);
  endSourceViewsForChat(sessionId);
}

/** Saved state keyed by the chat, so it never names a chat the host no longer has. */
function retireSessionState(scope: SessionChatScope): void {
  dropPersistedTranscriptRowHeights(scope);
  forgetTranscriptViewport(scope.sessionId);
  void forgetPersistedLastSessionSnapshot(scope).catch(() => undefined);
}

function sessionIdsForProject(deps: EntityRetireDeps, projectId: string): string[] {
  const ids = new Set<string>();
  for (const row of deps.recents.state.recents) {
    if (row.projectId === projectId && row.sessionId.trim()) {
      ids.add(row.sessionId.trim());
    }
  }
  for (const row of deps.attention.state.rows) {
    if (row.project_id === projectId && row.session_id.trim()) {
      ids.add(row.session_id.trim());
    }
  }
  return [...ids];
}
