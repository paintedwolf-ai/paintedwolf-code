import { beforeEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import type { Message, Session } from "../../api/types.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import {
  clearSessionChatCacheForTests,
  getSessionChatCache,
  putSessionChatCache,
} from "./session-chat-cache.ts";
import {
  bindCreatedSession,
  enrichCreatedSession,
  mergeCreatedSessionBootstrap,
  waitForSessionPromptable,
} from "./session-lifecycle.ts";

const resumeProjectEventsAfter = vi.hoisted(() => vi.fn());

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual = await importOriginal<
    typeof import("../../platform/connection/app-connection.ts")
  >();
  return {
    ...actual,
    resumeProjectEventsAfter,
  };
});

function preparingSession(id = "sess-1"): Session {
  return {
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "proj-1",
    posture: "build",
    status: "preparing",
    workspace_path: "/tmp/draft",
    created_at: "2025-01-01T00:00:00Z",
    activity_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
  };
}

function idleSession(id = "sess-1"): Session {
  return { ...preparingSession(id), status: "idle", title: "Triage" };
}

function boundaryMessage(): Message {
  return {
    id: "msg-boundary",
    role: "system",
    content: "",
    origin: "host",
    authority: "system",
    trust_tier: "trusted",
    kind: "workflow_boundary",
    visibility: "internal",
    workflow_run_id: "run-1",
    seq: 1,
    ord: 1,
    created_at: "2025-01-01T00:00:00Z",
  };
}

function userMessage(): Message {
  return {
    id: "msg-user",
    role: "user",
    content: "Build a triage dashboard",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    visibility: "transcript",
    workflow_run_id: "run-1",
    seq: 2,
    ord: 2,
    created_at: "2025-01-01T00:00:01Z",
  };
}

function bootstrapClient(opts: {
  session: Session;
  messages: Message[];
  watermark?: number;
}) {
  const watermark =
    opts.watermark ??
    opts.messages.reduce((max, m) => Math.max(max, m.seq ?? 0), 0);
  return {
    getSessionBootstrap: vi.fn(async () => ({
      event_cursor: "cursor-early",
      session: opts.session,
      transcript: {
        messages: opts.messages,
        watermark,
        turn_clocks: {},
        turn_loads: {},
      },
      progress: { steps: [], revision: 0 },
      turn_clock: { session_id: opts.session.id, active_ms: 0, work_ms: 0, running: false },
      findings: { findings: [], revision: 0 },
      queue: { queue_items: [], hold: false, sending: false, revision: 0 },
      coordinator: {},
      background_outputs: [],
      previews: [],
      workers: [],
      checkpoints: [],
    })),
    listWorkers: vi.fn().mockResolvedValue([]),
    getActiveWorkflowRun: vi.fn().mockResolvedValue({
      id: "run-1",
      session_id: opts.session.id,
      workflow_id: "implement",
      status: "running",
      current_phase: "boot",
      attach_policy: "session_create",
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    }),
    listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
    listWorkflows: vi.fn().mockResolvedValue([]),
    listBlueprints: vi.fn().mockResolvedValue([]),
    listCheckpoints: vi.fn().mockResolvedValue([]),
  };
}

describe("bindCreatedSession", () => {
  it("binds the create response as an empty foreground chat", () => {
    const appStore = createAppStore();
    const session = preparingSession();
    bindCreatedSession(appStore, session);
    expect(appStore.state.currentSession?.id).toBe(session.id);
    expect(appStore.state.currentSession?.status).toBe("preparing");
    expect(appStore.state.transcriptSessionId).toBe(session.id);
    expect(appStore.state.messages).toEqual([]);
  });
});

describe("mergeCreatedSessionBootstrap", () => {
  it("keeps a live promptable status over stale bootstrap preparing", () => {
    const live = idleSession();
    const bootstrap = preparingSession();
    expect(mergeCreatedSessionBootstrap(live, bootstrap).status).toBe("idle");
  });

  it("takes bootstrap status when live is still preparing", () => {
    const live = preparingSession();
    const bootstrap = idleSession();
    expect(mergeCreatedSessionBootstrap(live, bootstrap).status).toBe("idle");
  });
});

describe("enrichCreatedSession", () => {
  beforeEach(() => {
    clearSessionChatCacheForTests();
    resumeProjectEventsAfter.mockClear();
  });

  it("does not restart project SSE from the bootstrap cursor", async () => {
    const appStore = createAppStore();
    const session = idleSession();
    bindCreatedSession(appStore, preparingSession());
    const client = bootstrapClient({
      session,
      messages: [boundaryMessage()],
      watermark: 1,
    });

    await enrichCreatedSession(
      appStore,
      client as never,
      session.id,
      emptyProjects,
      { projectId: session.project_id },
    );

    expect(resumeProjectEventsAfter).not.toHaveBeenCalled();
    expect(appStore.state.currentSession?.status).toBe("idle");
    expect(appStore.state.messages).toHaveLength(1);
    expect(appStore.state.transcriptWatermark).toBe(1);
  });

  it("keeps live prompt rows that arrived before the early bootstrap page", async () => {
    const appStore = createAppStore();
    const session = idleSession();
    bindCreatedSession(appStore, preparingSession());
    // Model a prompt event during bootstrap.
    appStore.actions.upsertMessage(boundaryMessage());
    appStore.actions.upsertMessage(userMessage());
    expect(appStore.state.transcriptWatermark).toBe(2);

    const client = bootstrapClient({
      session,
      messages: [boundaryMessage()],
      watermark: 1,
    });

    await enrichCreatedSession(
      appStore,
      client as never,
      session.id,
      emptyProjects,
      { projectId: session.project_id },
    );

    expect(resumeProjectEventsAfter).not.toHaveBeenCalled();
    expect(appStore.state.transcriptWatermark).toBe(2);
    expect(appStore.state.messages.map((m) => m.id)).toEqual([
      "msg-boundary",
      "msg-user",
    ]);
    const visible = appStore.state.messages.filter(
      (m) => m.visibility !== "internal",
    );
    expect(visible).toHaveLength(1);
    expect(visible[0]?.content).toContain("triage dashboard");
  });

  it("drops a stale create cache row instead of restoring it", async () => {
    const appStore = createAppStore();
    const session = idleSession();
    bindCreatedSession(appStore, preparingSession());
    putSessionChatCache({
      scope: { projectId: session.project_id, sessionId: session.id },
      touchedAt: Date.now(),
      session: preparingSession(),
      transcript: {
        tail: [boundaryMessage()],
        pages: {},
        hasTailGap: false,
        hasMoreBefore: false,
        hasMoreAfter: false,
      },
      messages: [boundaryMessage()],
      transcriptWatermark: 1,
      turnClocks: [],
      turnLoads: [],
      workers: [],
      workerTranscripts: {},
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
      pendingCheckpoints: [],
    });

    const client = bootstrapClient({
      session,
      messages: [boundaryMessage(), userMessage()],
      watermark: 2,
    });

    await enrichCreatedSession(
      appStore,
      client as never,
      session.id,
      emptyProjects,
      { projectId: session.project_id },
    );

    expect(getSessionChatCache({
      projectId: session.project_id,
      sessionId: session.id,
    })?.transcriptWatermark).toBe(2);
    expect(appStore.state.messages.map((m) => m.seq)).toEqual([1, 2]);
  });

  it("installs the transcript without waiting for the recents write", async () => {
    const appStore = createAppStore();
    const session = idleSession();
    bindCreatedSession(appStore, preparingSession());
    const client = bootstrapClient({ session, messages: [boundaryMessage(), userMessage()] });
    // The recents write resolves on the persist debounce; never here.
    const registerSession = vi.fn(() => new Promise<void>(() => undefined));

    await enrichCreatedSession(
      appStore,
      client as never,
      session.id,
      emptyProjects,
      { projectId: session.project_id },
      { registerSession } as unknown as RecentsStore,
    );

    expect(registerSession).toHaveBeenCalledWith({
      projectId: session.project_id,
      sessionId: session.id,
      title: "Triage",
    });
    expect(appStore.state.messages.map((m) => m.id)).toEqual(["msg-boundary", "msg-user"]);
  });
});

describe("waitForSessionPromptable", () => {
  it("keeps one status read in flight while the host is slow", async () => {
    vi.useFakeTimers();
    try {
      const appStore = createAppStore();
      const session = preparingSession();
      bindCreatedSession(appStore, session);
      let finish!: (value: Session) => void;
      const response = new Promise<Session>((resolve) => { finish = resolve; });
      const getSession = vi.fn(() => response);
      const waiting = waitForSessionPromptable(appStore, stubClient({ getSession }), session.id);

      await vi.advanceTimersByTimeAsync(1_000);
      expect(getSession).toHaveBeenCalledOnce();
      finish({ ...session, status: "idle" });
      await waiting;
      expect(appStore.state.currentSession?.status).toBe("idle");
    } finally {
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });

  it("resolves immediately when the session is already idle", async () => {
    const appStore = createAppStore();
    const session = { ...preparingSession(), status: "idle" as const };
    bindCreatedSession(appStore, session);
    const client = {
      getSession: vi.fn(),
    };
    await waitForSessionPromptable(
      appStore,
      client as never,
      session.id,
      { timeoutMs: 1_000 },
    );
    expect(client.getSession).not.toHaveBeenCalled();
  });

  it("resolves when a polled getSession moves preparing to idle", async () => {
    const appStore = createAppStore();
    const session = preparingSession();
    bindCreatedSession(appStore, session);
    const client = {
      getSession: vi.fn().mockResolvedValue({ ...session, status: "idle" }),
    };

    await waitForSessionPromptable(
      appStore,
      client as never,
      session.id,
      { timeoutMs: 2_000 },
    );
    expect(appStore.state.currentSession?.status).toBe("idle");
    expect(client.getSession).toHaveBeenCalled();
  });

  it("rejects when the wait is superseded", async () => {
    const appStore = createAppStore();
    bindCreatedSession(appStore, preparingSession());
    const client = { getSession: vi.fn().mockResolvedValue(preparingSession()) };
    await expect(
      waitForSessionPromptable(appStore, client as never, "sess-1", {
        shouldApply: () => false,
        timeoutMs: 1_000,
      }),
    ).rejects.toThrow(/replaced/i);
  });
});
