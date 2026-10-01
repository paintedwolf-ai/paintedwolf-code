import { afterEach, describe, expect, it, vi } from "vitest";
import { createNoticeStore } from "../notices/notice-store.ts";
import { createAttentionStore } from "../store/attention-store.ts";
import { createRecentsStore } from "../store/recents-store.ts";
import {
  clearComposerDraft,
  composerDraftForSession,
  resetComposerDraftsForTests,
  setComposerDraft,
} from "../chat/composer/composer-drafts.ts";
import { createEntityRetire } from "./entity-retire.ts";
import { openFilesBuffer, resetProjectFilesForTests } from "../files/documents/project-files-buffers.ts";
import { projectFilesState } from "../files/documents/files-buffer-state.ts";

vi.mock("../platform/desktop/notifications.ts", () => ({
  cancelNotificationsForSessions: vi.fn(async () => undefined),
}));
vi.mock("../ui/paged-view/source-view-session.ts", () => ({
  endSourceViewsForChat: vi.fn(),
}));

import { cancelNotificationsForSessions } from "../platform/desktop/notifications.ts";
import { endSourceViewsForChat } from "../ui/paged-view/source-view-session.ts";
import { EMPTY_APP_STATE_V1 } from "../../shared/app-state-types.ts";
import { getAppStateSnapshot, resetAppStateSnapshotForTests } from "../store/app-state-snapshot.ts";
import { persistTranscriptViewportSnapshot } from "../chat/stream/transcript-viewport-state.ts";

describe("entity retire", () => {
  afterEach(() => {
    resetComposerDraftsForTests();
    resetProjectFilesForTests();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    vi.mocked(cancelNotificationsForSessions).mockClear();
    vi.mocked(endSourceViewsForChat).mockClear();
  });

  it("forgets everything Den keeps for a deleted chat, and nothing of its neighbours", async () => {
    const notices = createNoticeStore();
    const { bus } = attentionBus();
    const attention = createAttentionStore(() => null, bus);
    const recents = createRecentsStore();
    await recents.replaceRecents([
      { projectId: "p1", sessionId: "gone", title: "deleted" },
      { projectId: "p1", sessionId: "kept", title: "open" },
    ]);
    persistTranscriptViewportSnapshot("gone", { openKeys: [] });
    persistTranscriptViewportSnapshot("kept", { openKeys: [] });

    createEntityRetire({ notices, attention, recents }).session({ projectId: "p1", sessionId: "gone" });
    await Promise.resolve();

    expect(recents.state.recents.map((row) => row.sessionId)).toEqual(["kept"]);
    expect(Object.keys(getAppStateSnapshot().transcriptViewport?.byWindow.main?.bySession ?? {})).toEqual(["kept"]);
    expect(endSourceViewsForChat).toHaveBeenCalledWith("gone");
    expect(endSourceViewsForChat).not.toHaveBeenCalledWith("kept");
  });

  it("retires session-scoped notices, attention, drafts, and OS notifications", () => {
    const notices = createNoticeStore();
    notices.publish({ title: "failed", message: "x" }, {
      kind: "session",
      projectId: "p1",
      sessionId: "s1",
    });
    const { bus } = attentionBus();
    const attention = createAttentionStore(() => null, bus);
    attention.apply({
      rows: [
        {
          session_id: "s1",
          project_id: "p1",
          class: "needs_you",
          reason: "checkpoint",
          since_at: "2026-07-27T12:00:00Z",
        },
      ],
    });
    const recents = createRecentsStore();
    setComposerDraft("s1", "half typed");

    createEntityRetire({ notices, attention, recents }).session({ projectId: "p1", sessionId: "s1" });

    expect(notices.index().size).toBe(0);
    expect(attention.state.rows).toEqual([]);
    expect(composerDraftForSession("s1")).toBe("");
    expect(cancelNotificationsForSessions).toHaveBeenCalledWith(["s1"]);
  });

  it("retires every projection keyed by a deleted project", async () => {
    const notices = createNoticeStore();
    notices.publish({ title: "failed", message: "x" }, {
      kind: "session",
      projectId: "p1",
      sessionId: "s1",
    });
    const { bus } = attentionBus();
    const attention = createAttentionStore(() => null, bus);
    attention.apply({
      rows: [
        {
          session_id: "s1",
          project_id: "p1",
          class: "needs_you",
          reason: "ask",
          since_at: "2026-07-27T12:00:00Z",
        },
        {
          session_id: "keep",
          project_id: "p2",
          class: "running",
          reason: "turn_running",
          since_at: "2026-07-27T12:00:00Z",
        },
      ],
    });
    const recents = createRecentsStore();
    await recents.replaceRecents([
      { projectId: "p1", sessionId: "s1", title: "scratch" },
    ]);
    setComposerDraft("s1", "draft");
    setComposerDraft("keep", "other");
    openFilesBuffer("p1", { intent: "permanent", rootId: "removed-root", rootLabel: "removed", path: "file.ts" });
    const keptBuffer = openFilesBuffer("p2", { intent: "permanent", rootId: "keep-root", rootLabel: "keep", path: "file.ts" });

    createEntityRetire({ notices, attention, recents }).project("p1");

    expect(notices.index().size).toBe(0);
    expect(attention.state.rows.map((r) => r.session_id)).toEqual(["keep"]);
    expect(composerDraftForSession("s1")).toBe("");
    expect(composerDraftForSession("keep")).toBe("other");
    expect(projectFilesState("p1").order).toEqual([]);
    expect(projectFilesState("p2").order).toEqual([keptBuffer]);
    expect(cancelNotificationsForSessions).toHaveBeenCalledWith(["s1"]);
    clearComposerDraft("keep");
  });
});

function attentionBus() {
  const listeners = new Set<(v: { rows: never[] }) => void>();
  return {
    bus: {
      onAttentionEvent: (cb: (v: { rows: never[] }) => void) => {
        listeners.add(cb);
        return () => listeners.delete(cb);
      },
    },
  };
}
