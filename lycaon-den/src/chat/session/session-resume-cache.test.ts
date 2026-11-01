import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { resumeChatSession } from "./session-lifecycle.ts";
import { clearSessionChatCacheForTests, getSessionChatCache, putSessionChatCache } from "./session-chat-cache.ts";
import type { Session } from "../../api/types.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { withBootstrap } from "./session-lifecycle-test-fixture.ts";
import * as appConnection from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";

describe("resumeChatSession cache", () => {
  it("resumeChatSession restores a cached session then reconciles in the background", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    const ambientRun = {
      id: "run-1",
      session_id: "sess-a",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    putSessionChatCache({
      scope: { projectId: "proj-1", sessionId: "sess-a" },
      touchedAt: 1,
      session: {
        id: "sess-a",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
      messages: [
        {
          id: "m1",
          role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
          content: "cached turn",
          created_at: "t",
          seq: 1,
          workflow_run_id: "run-1",
        },
      ],
      transcript: {
        tail: [
          {
            id: "m1",
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content: "cached turn",
            created_at: "t",
            seq: 1,
            workflow_run_id: "run-1",
          },
        ],
        pages: {},
        hasTailGap: false,
        hasMoreBefore: false,
        hasMoreAfter: false,
      },
      transcriptWatermark: 1,
      turnClocks: [],
      turnLoads: [],
      workers: [],
      workerTranscripts: {},
      activeWorkflowRun: ambientRun,
      workflowRuns: [ambientRun],
      workflowCatalog: [],
      blueprints: [],
      pendingCheckpoints: [],
    });

    const client = withBootstrap({
      getSession: vi.fn(async () => ({
        id: "sess-a",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      })),
      listSessionMessages: vi.fn(async () => ({
        messages: [
          {
            id: "m1",
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content: "cached turn",
            created_at: "t",
            seq: 1,
            workflow_run_id: "run-1",
          },
        ],
        watermark: 1,
        turn_clocks: {},
        turn_loads: {},
      })),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
      listCheckpoints: vi.fn(async () => []),
    });

    await resumeChatSession(appStore, client, "sess-a", emptyProjects, undefined, {
      projectId: "proj-1",
    });

    expect(appStore.state.messages[0]?.content).toBe("cached turn");
    expect(client.listSessionMessages).toHaveBeenCalled();
  });

  it("resumeChatSession aborts store writes when shouldApply becomes false", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    let resolveSession!: (session: Session) => void;
    const sessionGate = new Promise<Session>((resolve) => {
      resolveSession = resolve;
    });
    let apply = true;
    const client = withBootstrap({
      getSession: vi.fn(async () => sessionGate),
      listSessionMessages: vi.fn(async () => ({ messages: [], watermark: 0, turn_clocks: {}, turn_loads: {} })),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => undefined),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
      listCheckpoints: vi.fn(async () => []),
    });

    const pending = resumeChatSession(
      appStore,
      client,
      "sess-stale",
      emptyProjects,
      undefined,
      {
        projectId: "proj-1",
        shouldApply: () => apply,
      },
    );
    apply = false;
    resolveSession({
      id: "sess-stale",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    await pending;
    expect(appStore.state.currentSession).toBeUndefined();
  });

  it("resumeChatSession drops a cache row that fails to rebind and loads from the server", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    putSessionChatCache({
      scope: { projectId: "proj-1", sessionId: "sess-a" },
      touchedAt: 1,
      // Corrupt / mismatched session payload — restore cannot bind sess-a.
      session: {
        id: "",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
      messages: [],
      transcript: {
        tail: [],
        pages: {},
        hasTailGap: false,
        hasMoreBefore: false,
        hasMoreAfter: false,
      },
      transcriptWatermark: 0,
      turnClocks: [],
      turnLoads: [],
      workers: [],
      workerTranscripts: {},
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
      pendingCheckpoints: [],
    });

    const ambientRun = {
      id: "run-1",
      session_id: "sess-a",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const client = withBootstrap({
      getSession: vi.fn(async () => ({
        id: "sess-a",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      })),
      listSessionMessages: vi.fn(async () => ({ messages: [], watermark: 0, turn_clocks: {}, turn_loads: {} })),
      listWorkers: vi.fn(async () => []),
      getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
      listCheckpoints: vi.fn(async () => []),
    });

    await resumeChatSession(appStore, client, "sess-a", emptyProjects, undefined, {
      projectId: "proj-1",
    });

    expect(client.getSession).toHaveBeenCalled();
    expect(client.listSessionMessages).toHaveBeenCalled();
    expect(appStore.state.currentSession?.id).toBe("sess-a");
    expect(
      getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" })?.session.id,
    ).toBe("sess-a");
  });

  it("reconciles a cached cross-project resume when bootstrap has a running background process with null chunks", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    const resumeSpy = vi.spyOn(appConnection, "resumeProjectEventsAfter");
    try {
      const session: Session = {
        id: "sess-a",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      };
      const staleMessage = {
        id: "m1",
        role: "user" as const,
        origin: "user" as const,
        authority: "user" as const,
        trust_tier: "trusted" as const,
        content: "before leaving",
        created_at: "t",
        seq: 1,
      };
      const freshMessage = {
        id: "m2",
        role: "user" as const,
        origin: "user" as const,
        authority: "user" as const,
        trust_tier: "trusted" as const,
        content: "delivered while away",
        created_at: "t2",
        seq: 2,
      };
      putSessionChatCache({
        scope: { projectId: "proj-1", sessionId: "sess-a" },
        touchedAt: 1,
        session,
        messages: [staleMessage],
        transcript: {
          tail: [staleMessage],
          pages: {},
          hasTailGap: false,
          hasMoreBefore: false,
          hasMoreAfter: false,
        },
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

      appStore.actions.clearChatForSessionSwitch("*");
      const client = stubClient({
        getSessionBootstrap: vi.fn(async () => ({
          event_cursor: "cursor-after-away",
          session,
          transcript: {
            messages: [staleMessage, freshMessage],
            watermark: 2,
            turn_clocks: {},
            turn_loads: {},
          },
          progress: { steps: [], revision: 0 },
          turn_clock: { session_id: session.id, active_ms: 0, work_ms: 0, running: false },
          findings: { findings: [], revision: 0 },
          queue: { queue_items: [], hold: false, sending: false, revision: 0 },
          coordinator: {},
          background_outputs: [
            {
              process_id: "proc-silent",
              running: true,
              chunks: null as unknown as [],
            },
          ],
          previews: [],
          workers: [],
          checkpoints: [],
        })),
        listWorkers: vi.fn(async () => []),
        getBoard: vi.fn(async () => ({ tasks: [], updated_at: "t" })),
        getActiveWorkflowRun: vi.fn(async () => undefined),
        listSessionWorkflowRuns: vi.fn(async () => ({ runs: [] })),
        listWorkflows: vi.fn(async () => []),
        listBlueprints: vi.fn(async () => []),
      });

      await resumeChatSession(appStore, client, "sess-a", emptyProjects, undefined, {
        projectId: "proj-1",
      });

      expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1", "m2"]);
      expect(resumeSpy).toHaveBeenCalledWith("cursor-after-away");
      expect(appStore.state.chatHydrationLock).toBeUndefined();
      expect(
        getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" })?.messages.map(
          (m) => m.id,
        ),
      ).toEqual(["m1", "m2"]);
    } finally {
      resumeSpy.mockRestore();
    }
  });

  it("does not overwrite the session chat cache when reconcile fails", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    const session: Session = {
      id: "sess-a",
      title: "Cached title",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    };
    const cachedMessage = {
      id: "m1",
      role: "user" as const,
      origin: "user" as const,
      authority: "user" as const,
      trust_tier: "trusted" as const,
      content: "cached turn",
      created_at: "t",
      seq: 1,
    };
    putSessionChatCache({
      scope: { projectId: "proj-1", sessionId: "sess-a" },
      touchedAt: 1,
      session,
      messages: [cachedMessage],
      transcript: {
        tail: [cachedMessage],
        pages: {},
        hasTailGap: false,
        hasMoreBefore: false,
        hasMoreAfter: false,
      },
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

    const client = stubClient({
      getSessionBootstrap: vi.fn(async () => ({
        event_cursor: "cursor-1",
        session: { ...session, title: "Half-applied title" },
        transcript: {
          messages: [
            {
              ...cachedMessage,
              id: "m2",
              content: "needs missing run",
              workflow_run_id: "run-fail",
            },
          ],
          watermark: 2,
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
      })),
      getActiveWorkflowRun: vi.fn(async () => undefined),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [] })),
      getWorkflowRun: vi.fn(async () => {
        throw new Error("reconcile failed");
      }),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    await expect(
      resumeChatSession(appStore, client, "sess-a", emptyProjects, undefined, {
        projectId: "proj-1",
      }),
    ).rejects.toThrow("reconcile failed");

    const retained = getSessionChatCache({ projectId: "proj-1", sessionId: "sess-a" });
    expect(retained?.session.title).toBe("Cached title");
    expect(retained?.messages.map((m) => m.id)).toEqual(["m1"]);
  });

});
