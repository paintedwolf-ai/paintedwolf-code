import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { registerProjectsStore } from "../../platform/connection/app-connection.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createShellStore } from "../../store/shell-store.ts";
import {
  reconcileActiveScope,
  reconcileRecents,
  reconcileRecentsWithStores,
  refreshCodeScanCache,
} from "./session-reconcile.ts";
import { LycaonApiError } from "../../api/http.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import type { ActivityEvent, Message, Session, SessionBootstrap } from "../../api/types.ts";

function bootstrap(
  session: Session,
  messages: Message[] = [],
  watermark = 0,
): SessionBootstrap {
  return {
    event_cursor: "",
    session,
    transcript: {
      messages,
      watermark,
      turn_clocks: {},
      turn_loads: {},
    },
    progress: { steps: [], revision: 0 },
    turn_clock: { session_id: session.id, active_ms: 0, work_ms: 0, running: false },
    findings: { findings: [], revision: 0 },
    queue: { queue_items: [], hold: false, sending: false, revision: 0 },
    coordinator: {},
    background_outputs: [],
    previews: [],
    workers: [],
    checkpoints: [],
  };
}

describe("reconcileActiveScope", () => {
  it.each(["running_tool", "awaiting_wake", "preparing_context"] as const)(
    "replaces missed %s activity edges from the host snapshot without touching another chat",
    async (kind) => {
      const store = createAppStore();
      const session: Session = { id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", workspace_path: "/project", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" };
      store.actions.setCurrentSession(session);
      const lease: ActivityEvent = { activity_id: "old", session_id: session.id, kind, status: "active", started_at: "2026-09-15T00:00:00Z" };
      store.actions.setActivity(lease);
      store.actions.setActivity({ ...lease, session_id: "other" });
      const snapshot = bootstrap(session);
      const client = stubClient({
        getSessionBootstrap: async () => snapshot,
        getActiveWorkflowRun: async () => undefined,
        listSessionWorkflowRuns: async () => ({ runs: [] }),
        listWorkflows: async () => [], listBlueprints: async () => [],
      });
      await reconcileActiveScope(store, client, [], { resumeVisible: true });
      expect(store.state.sessionActivity.s1?.activities).toBeUndefined();
      expect(store.state.sessionActivity.other?.activities?.old).toBeDefined();
      snapshot.activities = [{ ...lease, activity_id: "current" }];
      await reconcileActiveScope(store, client, [], { resumeVisible: true });
      expect(Object.keys(store.state.sessionActivity.s1?.activities ?? {})).toEqual(["current"]);
      // A real wake lease remains live across an idle session snapshot.
      expect(store.state.currentSession?.status).toBe("idle");
    },
  );

  it("does not clear a new session while missing-session cleanup awaits persistence", async () => {
    const store = createAppStore();
    const session: Session = { id: "a", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", workspace_path: "/project", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" };
    store.actions.setCurrentSession(session);
    let entered!: () => void;
    let finish!: () => void;
    const started = new Promise<void>((resolve) => { entered = resolve; });
    const persistence = new Promise<void>((resolve) => { finish = resolve; });
    const old = reconcileActiveScope(store, stubClient({ getSessionBootstrap: async () => { throw new LycaonApiError("Missing", 404, "session_not_found"); } }), [], {
      onSessionGone: async () => { entered(); await persistence; },
    });
    await started;
    store.actions.setCurrentSession({ ...session, id: "b" });
    store.actions.installTranscriptBaseline("b", [{ id: "b-message", role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: "Keep", created_at: "t", seq: 1 }], 1);
    finish();
    await old;
    expect(store.state.currentSession?.id).toBe("b");
    expect(store.state.messages.map((message) => message.id)).toEqual(["b-message"]);
  });

  it.each([true, false])("keeps the latest bootstrap when older finishes first: %s", async (olderFirst) => {
    const store = createAppStore();
    const session: Session = { id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", workspace_path: "/project", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" };
    store.actions.setCurrentSession(session);
    let finishOld!: (value: SessionBootstrap) => void;
    let finishNew!: (value: SessionBootstrap) => void;
    const oldResponse = new Promise<SessionBootstrap>((resolve) => { finishOld = resolve; });
    const newResponse = new Promise<SessionBootstrap>((resolve) => { finishNew = resolve; });
    const clientFor = (response: Promise<SessionBootstrap>) => stubClient({
      getSessionBootstrap: () => response,
      getActiveWorkflowRun: async () => undefined,
      listSessionWorkflowRuns: async () => ({ runs: [] }),
      listWorkflows: async () => [],
      listBlueprints: async () => [],
    });
    const oldRead = reconcileActiveScope(store, clientFor(oldResponse), []);
    const newRead = reconcileActiveScope(store, clientFor(newResponse), []);
    if (olderFirst) {
      finishOld(bootstrap({ ...session, title: "Old" }));
      await oldRead;
      expect(store.state.currentSession?.title).toBeUndefined();
      finishNew(bootstrap({ ...session, title: "New" }));
    } else {
      finishNew(bootstrap({ ...session, title: "New" }));
      await newRead;
      finishOld(bootstrap({ ...session, title: "Old" }));
    }
    await Promise.all([oldRead, newRead]);
    expect(store.state.currentSession?.title).toBe("New");
  });

  it("discards a bootstrap after switching away and back to the same session", async () => {
    const store = createAppStore();
    const session: Session = {
      id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", workspace_path: "/project", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    };
    store.actions.setCurrentSession(session);
    let finish!: (value: SessionBootstrap) => void;
    const response = new Promise<SessionBootstrap>((resolve) => { finish = resolve; });
    const old = reconcileActiveScope(store, stubClient({ getSessionBootstrap: () => response }), []);
    store.actions.setCurrentSession({ ...session, id: "s2" });
    store.actions.setCurrentSession({ ...session, title: "Reopened" });
    finish(bootstrap({ ...session, title: "Old" }));
    await old;
    expect(store.state.currentSession?.title).toBe("Reopened");
  });

  it("opens the transcript while the board is pending and reports enrichment failure independently", async () => {
    const store = createAppStore();
    const session: Session = {
      id: "opening", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    };
    store.actions.setCurrentSession(session);
    let rejectBoard!: (err: Error) => void;
    const board = new Promise<never>((_, reject) => { rejectBoard = reject; });
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => bootstrap(session, [
        { id: "opening-message", role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: "hello", created_at: "t", seq: 1 },
      ], 1)),
      getBoard: vi.fn(() => board),
      getActiveWorkflowRun: vi.fn(async () => undefined),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });
    await reconcileActiveScope(store, client, [wireProject("/tmp/p", session.project_id)], { resumeVisible: true });
    expect(store.state.chatHydrationLock).toBeUndefined();
    expect(store.state.messages[0]?.content).toBe("hello");
    expect(store.state.boardLoad.state).toBe("loading");
    expect(client.getBoard).toHaveBeenCalledTimes(1);
    rejectBoard(new Error("Board unavailable"));
    await vi.waitFor(() => expect(store.state.boardLoad.state).toBe("error"));
    expect(store.state.currentSession?.id).toBe(session.id);
    expect(store.state.messages[0]?.content).toBe("hello");
  });

  it("refreshes session-scoped slices from the server", async () => {
    const store = createAppStore();
    const projects = createProjectsStore(() => null, {
      onProjectEvent: () => () => {},
    });
    projects.load([wireProject("/tmp/p")]);
    registerProjectsStore(projects);
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setWorkers([
      {
        id: "w-stale",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "complete",
        created_at: "t",
      },
    ]);
    store.actions.applyWorkerTranscriptRows("w-stale", [
      { id: "m1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "old", created_at: "t" },
    ]);

    const ambientRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () =>
        bootstrap(
          {
            id: "sess-1",
            owner_person_id: "00000000-0000-4000-8000-000000000002",
            project_id: "00000000-0000-4000-8000-000000000001",
            workspace_path: "/tmp/p",
            posture: "build",
            status: "busy",
            created_at: "t",
            activity_at: "t",
            updated_at: "t2",
          },
          [
            {
              id: "m2",
              role: "user",
              origin: "user" as const,
              authority: "user" as const,
              trust_tier: "trusted" as const,
              content: "fresh",
              workflow_run_id: "run-1",
              created_at: "t2",
            },
          ],
        ),
      ),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    await reconcileActiveScope(store, client, emptyProjects);

    expect(store.state.currentSession?.status).toBe("busy");
    expect(store.state.messages).toHaveLength(1);
    expect(store.state.workers).toEqual([]);
    expect(store.state.workerTranscripts).toEqual({});
  });

  it("clears foreground when session was deleted server-side", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "gone",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const gone = vi.fn();
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => {
        throw new LycaonApiError("missing", 404, "session_not_found");
      }),
    });

    await expect(
      reconcileActiveScope(store, client, emptyProjects, {
        onSessionGone: gone,
      }),
    ).rejects.toBeInstanceOf(LycaonApiError);
    expect(gone).toHaveBeenCalledWith({
      projectId: "00000000-0000-4000-8000-000000000001",
      sessionId: "gone",
    });
    expect(store.state.currentSession).toBeUndefined();
  });

  it("resumeVisible keeps the transcript mounted while reconciling", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.installTranscriptBaseline(
      "sess-1",
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "cached", created_at: "t", seq: 1 }],
      1,
    );

    const ambientRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    let resolveMessages!: () => void;
    const messagesGate = new Promise<void>((resolve) => {
      resolveMessages = resolve;
    });
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => {
        await messagesGate;
        return bootstrap(
          {
            id: "sess-1",
            owner_person_id: "00000000-0000-4000-8000-000000000002",
            project_id: "00000000-0000-4000-8000-000000000001",
            workspace_path: "/tmp/p",
            posture: "build",
            status: "idle",
            created_at: "t",
            activity_at: "t",
            updated_at: "t",
          },
          [
            { id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "cached", created_at: "t", seq: 1 },
          ],
          1,
        );
      }),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    const reconcile = reconcileActiveScope(store, client, emptyProjects, {
      resumeVisible: true,
    });
    expect(store.state.chatHydrationLock).toBe("sess-1");
    expect(store.state.messages).toHaveLength(1);
    resolveMessages();
    await reconcile;
    expect(store.state.chatHydrationLock).toBeUndefined();
  });

  it("drops stale resumeVisible writes after the foreground session changes", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.installTranscriptBaseline(
      "sess-a",
      [{ id: "m-a", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "a", created_at: "t", seq: 1 }],
      1,
    );

    let resolveMessages!: () => void;
    const messagesGate = new Promise<void>((resolve) => {
      resolveMessages = resolve;
    });
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => {
        await messagesGate;
        return bootstrap(
          {
            id: "sess-a",
            owner_person_id: "00000000-0000-4000-8000-000000000002",
            project_id: "00000000-0000-4000-8000-000000000001",
            workspace_path: "/tmp/p",
            posture: "build",
            status: "idle",
            created_at: "t",
            activity_at: "t",
            updated_at: "t",
          },
          [
            { id: "m-a", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "a", created_at: "t", seq: 1 },
          ],
          1,
        );
      }),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => undefined),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    const stale = reconcileActiveScope(store, client, emptyProjects, {
      resumeVisible: true,
    });
    expect(store.state.chatHydrationLock).toBe("sess-a");

    store.actions.setCurrentSession({
      id: "sess-b",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.beginSessionResumeSwitch("sess-b");
    store.actions.installTranscriptBaseline(
      "sess-b",
      [{ id: "m-b", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "b", created_at: "t", seq: 1 }],
      1,
    );
    store.actions.completeChatSessionHydration();

    resolveMessages();
    await stale;

    expect(store.state.currentSession?.id).toBe("sess-b");
    expect(store.state.messages[0]?.content).toBe("b");
    expect(store.state.chatHydrationLock).toBeUndefined();
  });

  it("skips reconnect reconcile while a different session controls the hydration lock", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.beginSessionResumeSwitch("sess-b");

    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => bootstrap(store.state.currentSession!)),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
    });

    await reconcileActiveScope(store, client, emptyProjects, { resumeVisible: true });
    expect(client.getSessionBootstrap).not.toHaveBeenCalled();
    expect(store.state.chatHydrationLock).toBe("sess-b");
  });

  it("skips reconnect reconcile while a cross-project wildcard lock is held", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.clearChatForSessionSwitch("*");

    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => bootstrap(store.state.currentSession!)),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
    });

    await reconcileActiveScope(store, client, emptyProjects, { resumeVisible: true });
    expect(client.getSessionBootstrap).not.toHaveBeenCalled();
    expect(store.state.chatHydrationLock).toBe("*");
  });

  it("switch-held hydrate may proceed under a wildcard lock", async () => {
    const store = createAppStore();
    store.actions.clearChatForSessionSwitch("*");
    // The switch rebinds currentSession before reconciliation.
    store.actions.setCurrentSession({
      id: "sess-b",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });

    const ambientRun = {
      id: "run-1",
      session_id: "sess-b",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => bootstrap(store.state.currentSession!)),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    await reconcileActiveScope(store, client, emptyProjects, {
      resumeVisible: true,
      fromSessionSwitch: true,
    });
    expect(client.getSessionBootstrap).toHaveBeenCalled();
    expect(store.state.chatHydrationLock).toBeUndefined();
  });

  it("resumeVisible skips beginSessionResumeSwitch when the lock already matches", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.beginSessionResumeSwitch("sess-1");
    store.actions.setPendingCheckpoints("sess-1", store.state.sessionViewEpoch, store.state.checkpointEventEpoch, [
      {
        checkpointId: "cp-1",
        sessionId: "sess-1",
        kind: "tool_approval",
        status: "pending",
        issuedAt: "t",
      },
    ]);

    const ambientRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => ({
        ...bootstrap(store.state.currentSession!),
        checkpoints: [
          {
            id: "cp-1",
            session_id: "sess-1",
            kind: "tool_approval" as const,
            status: "pending" as const,
            issued_at: "t",
          },
        ],
      })),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    await reconcileActiveScope(store, client, emptyProjects, { resumeVisible: true });
    expect(store.state.pendingCheckpoints).toHaveLength(1);
  });
});

describe("reconcileRecents", () => {
  it("drops rows whose sessions return 404", async () => {
    const client = stubClient({
      getSession: vi.fn(async (id: string) => {
        if (id === "gone") {
          throw new LycaonApiError("missing", 404, "session_not_found");
        }
        return { id };
      }),
    });

    const result = await reconcileRecents(client, [
      { projectId: "proj-a", sessionId: "live", title: "Live" },
      { projectId: "proj-a", sessionId: "gone", title: "Gone" },
    ]);

    expect(result.kept).toEqual([
      { projectId: "proj-a", sessionId: "live", title: "Live" },
    ]);
    expect(result.gone).toEqual([
      { projectId: "proj-a", sessionId: "gone" },
    ]);
  });

  it("keeps rows when the probe fails for a non-404 reason", async () => {
    const row = { projectId: "proj-a", sessionId: "maybe", title: "Maybe" };
    const client = stubClient({
      getSession: vi.fn(async () => {
        throw new LycaonApiError("offline", 500, "internal_error");
      }),
    });

    const result = await reconcileRecents(client, [row]);
    expect(result).toEqual({ kept: [row], gone: [] });
  });
});

describe("reconcileRecentsWithStores", () => {
  it("does not prune recents after the backend changes during the read", async () => {
    let rejectRead!: (error: Error) => void;
    const client = stubClient({ getSession: () => new Promise((_resolve, reject) => { rejectRead = reject; }) });
    const rows = [{ projectId: "project", sessionId: "gone", title: "Gone", lastActivityAt: 1 }];
    const replaceRecents = vi.fn(async () => undefined);
    const shell = { evictConversation: vi.fn() };
    let current = true;
    const reading = reconcileRecentsWithStores(client, { state: { loaded: true, recents: rows }, replaceRecents }, shell, () => current);
    current = false;
    rejectRead(new LycaonApiError("Missing", 404, "session_not_found"));
    await reading;
    expect(replaceRecents).not.toHaveBeenCalled();
    expect(shell.evictConversation).not.toHaveBeenCalled();
  });

  it("does not evict a new scope while old pruning persists", async () => {
    const client = stubClient({ getSession: async () => { throw new LycaonApiError("Missing", 404, "session_not_found"); } });
    const rows = [{ projectId: "project", sessionId: "gone", title: "Gone", lastActivityAt: 1 }];
    let finish!: () => void;
    const replaceRecents = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const shell = { evictConversation: vi.fn() };
    let current = true;
    const reading = reconcileRecentsWithStores(client, { state: { loaded: true, recents: rows }, replaceRecents }, shell, () => current);
    await vi.waitFor(() => expect(replaceRecents).toHaveBeenCalledOnce());
    current = false;
    finish();
    await reading;
    expect(shell.evictConversation).not.toHaveBeenCalled();
  });

  it("persists pruned recents and evicts shell foreground", async () => {
    const client = stubClient({
      getSession: vi.fn(async (id: string) => {
        if (id === "gone") {
          throw new LycaonApiError("missing", 404, "session_not_found");
        }
        return { id };
      }),
    });

    const recents = createRecentsStore();
    await recents.load();
    await recents.registerSession({
      projectId: "proj-a",
      sessionId: "live",
      title: "Live",
    });
    await recents.registerSession({
      projectId: "proj-a",
      sessionId: "gone",
      title: "Gone",
    });
    const shell = createShellStore("connected");
    shell.commitStageScope({ projectId: "proj-a", sessionId: "gone" });

    const gone = await reconcileRecentsWithStores(client, recents, shell, () => true);

    expect(gone).toEqual([{ projectId: "proj-a", sessionId: "gone" }]);
    expect(recents.state.recents.map((r) => r.sessionId)).toEqual(["live"]);
    expect(shell.state.foreground).toBeNull();
  });
});

describe("refreshCodeScanCache", () => {
  it("clears cached scan when the row no longer exists", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", owner_person_id: "owner", activity_at: "2026-09-28T00:00:00Z", project_id: "project", posture: "build", status: "idle", created_at: "2026-09-28T00:00:00Z", updated_at: "2026-09-28T00:00:00Z" });
    store.actions.addCodeScan({
      scan_id: "scan-old",
      status: "complete",
      categories: ["sast"],
      findings_count: 0,
      long_running: false,
    });
    const client = stubClient({
      getCodeScan: vi.fn(async () => {
        throw new LycaonApiError("missing", 404, "scan_not_found");
      }),
    });

    await refreshCodeScanCache(store, client);
    expect(client.getCodeScan).toHaveBeenCalledWith("project", "scan-old", "summary");
    expect(store.state.latestCodeScan).toBeUndefined();
  });

  it("keeps cached scan when the row still exists", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", owner_person_id: "owner", activity_at: "2026-09-28T00:00:00Z", project_id: "project", posture: "build", status: "idle", created_at: "2026-09-28T00:00:00Z", updated_at: "2026-09-28T00:00:00Z" });
    store.actions.addCodeScan({
      scan_id: "scan-live",
      status: "complete",
      categories: ["sast"],
      findings_count: 2,
      long_running: false,
    });
    const client = stubClient({
      getCodeScan: vi.fn(async () => ({
        id: "scan-live",
        status: "complete",
        categories: ["sast"],
      })),
    });

    await refreshCodeScanCache(store, client);
    expect(client.getCodeScan).toHaveBeenCalledWith("project", "scan-live", "summary");
    expect(store.state.latestCodeScan?.scan_id).toBe("scan-live");
  });
});
