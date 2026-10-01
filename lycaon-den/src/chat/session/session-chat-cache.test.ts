import { describe, expect, it, beforeEach } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import type { Session } from "../../api/types.ts";
import {
  SESSION_CHAT_CACHE_MAX,
  captureSessionChatFromStore,
  clearSessionChatCacheForTests,
  dropSessionChatCacheForProject,
  dropSessionChatCacheForSession,
  getSessionChatCache,
  putSessionChatCache,
  rememberSessionChatFromStore,
  restoreCachedSessionChat,
} from "./session-chat-cache.ts";
import type { SessionChatSnapshot } from "./session-chat-snapshot.ts";

const demoSession: Session = {
  id: "sess-a",
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "proj-1",
  workspace_path: "/tmp/demo",
  title: "Demo",
  posture: "build",
  status: "idle",
  created_at: "2025-01-01T00:00:00Z",
  activity_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
};

function sampleSnapshot(
  sessionId: string,
  touchedAt: number,
  projectId = "proj-1",
): SessionChatSnapshot {
  const messages = [
    { id: "m1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: `hello ${sessionId}`, created_at: "t", seq: 1 },
  ];
  return {
    scope: { projectId, sessionId },
    touchedAt,
    session: { ...demoSession, id: sessionId, project_id: projectId },
    transcript: {
      tail: messages,
      pages: {},
      hasTailGap: false,
      hasMoreBefore: false,
      hasMoreAfter: false,
    },
    messages,
    transcriptWatermark: 1,
    turnClocks: [],
    turnLoads: [],
    workers: [],
    workerTranscripts: {},
    workflowRuns: [],
    workflowCatalog: [],
    blueprints: [],
    pendingCheckpoints: [],
  };
}

describe("session-chat-cache", () => {
  beforeEach(() => {
    clearSessionChatCacheForTests();
  });

  it("evicts oldest entries past SESSION_CHAT_CACHE_MAX", () => {
    for (let i = 0; i < SESSION_CHAT_CACHE_MAX + 2; i++) {
      putSessionChatCache(sampleSnapshot(`sess-${i}`, i));
    }
    expect(getSessionChatCache({ projectId: "proj-1", sessionId: "sess-0" })).toBeUndefined();
    expect(getSessionChatCache({ projectId: "proj-1", sessionId: "sess-1" })).toBeUndefined();
    expect(
      getSessionChatCache({
        projectId: "proj-1",
        sessionId: `sess-${SESSION_CHAT_CACHE_MAX + 1}`,
      }),
    ).toBeDefined();
  });

  it("captureSessionChatFromStore requires matching transcript session", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(demoSession);
    store.actions.installTranscriptBaseline("sess-a", [], 0);
    const snap = captureSessionChatFromStore(store, {
      projectId: "proj-1",
      sessionId: "sess-a",
    });
    expect(snap?.messages).toEqual([]);
    store.actions.installTranscriptBaseline("sess-b", [], 0);
    expect(
      captureSessionChatFromStore(store, {
        projectId: "proj-1",
        sessionId: "sess-a",
      }),
    ).toBeNull();
  });

  it("restoreSessionChatSnapshot restores transcript and workflow slices", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ ...demoSession, id: "sess-b" });
    store.actions.installTranscriptBaseline("sess-b", [], 0);

    const cached = sampleSnapshot("sess-a", 1);
    store.actions.restoreSessionChatSnapshot(cached);

    expect(store.state.currentSession?.id).toBe("sess-a");
    expect(store.state.messages[0]?.content).toBe("hello sess-a");
    expect(store.state.transcriptSessionId).toBe("sess-a");
  });

  it("rememberSessionChatFromStore round-trips through getSessionChatCache", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(demoSession);
    store.actions.installTranscriptBaseline(
      "sess-a",
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "cached", created_at: "t", seq: 1 }],
      1,
    );
    rememberSessionChatFromStore(store);
    const hit = getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" });
    expect(hit?.messages[0]?.content).toBe("cached");
  });

  it("restoreCachedSessionChat binds a cached transcript", () => {
    const store = createAppStore();
    putSessionChatCache(sampleSnapshot("sess-a", 1));
    store.actions.setCurrentSession({ ...demoSession, id: "sess-b" });
    store.actions.installTranscriptBaseline("sess-b", [], 0);
    expect(
      restoreCachedSessionChat(store, { projectId: "proj-1", sessionId: "sess-a" }),
    ).toBe(true);
    expect(store.state.currentSession?.id).toBe("sess-a");
    expect(store.state.transcriptSessionId).toBe("sess-a");
    expect(store.state.messages[0]?.content).toBe("hello sess-a");
  });

  it("cached snapshot survives clearChat dropping currentSession (store proxy unwrap)", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(demoSession);
    store.actions.installTranscriptBaseline(
      "sess-a",
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "keep me", created_at: "t", seq: 1 }],
      1,
    );
    rememberSessionChatFromStore(store);
    store.actions.clearChatForSessionSwitch("*");
    expect(store.state.currentSession).toBeUndefined();

    const hit = getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" });
    expect(hit?.session.id).toBe("sess-a");
    store.actions.restoreSessionChatSnapshot(hit!);
    expect(store.state.currentSession?.id).toBe("sess-a");
    expect(store.state.messages[0]?.content).toBe("keep me");
  });

  it("dropSessionChatCacheForProject evicts every session row for the project", () => {
    putSessionChatCache(sampleSnapshot("sess-a", 1, "proj-1"));
    putSessionChatCache(sampleSnapshot("sess-b", 2, "proj-1"));
    putSessionChatCache(sampleSnapshot("sess-c", 3, "proj-2"));
    dropSessionChatCacheForProject("proj-1");
    expect(getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" })).toBeUndefined();
    expect(getSessionChatCache({ projectId: "proj-2", sessionId: "sess-c" })).toBeDefined();
  });

  it("dropSessionChatCacheForSession evicts that session across projects", () => {
    putSessionChatCache(sampleSnapshot("sess-a", 1, "proj-1"));
    putSessionChatCache(sampleSnapshot("sess-a", 2, "proj-2"));
    putSessionChatCache(sampleSnapshot("sess-b", 3, "proj-1"));
    dropSessionChatCacheForSession("sess-a");
    expect(getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" })).toBeUndefined();
    expect(getSessionChatCache({ projectId: "proj-2", sessionId: "sess-a" })).toBeUndefined();
    expect(getSessionChatCache({ projectId: "proj-1", sessionId: "sess-b" })).toBeDefined();
  });
});
