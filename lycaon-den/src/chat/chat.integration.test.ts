import { stubClient } from "../test/client-fixture.ts";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { wireProject } from "../api/mocks/project-fixture.ts";
import { createAppStore } from "../store/app-state.ts";
import { createProjectsStore } from "../store/projects-store.ts";
import { createRecentsStore } from "../store/recents-store.ts";
import {
  refreshCreatedSessionBoard,
  sendChatPrompt,
} from "./session/session-lifecycle.ts";
import { applyMessageEvent } from "./transcript/projection/message-events.ts";
import { emptyProjects } from "../test/projects-fixture.ts";

const DEMO_PROJECT_ID = "00000000-0000-4000-8000-000000000001";

function testProjectsStore(seed: ReturnType<typeof wireProject>[] = []) {
  const store = createProjectsStore(() => null, {
    onProjectEvent: () => () => {},
  });
  if (seed.length > 0) store.load(seed);
  return store;
}

describe("chat integration", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("keeps optimistic sends outside host transcript state and reconciles on SSE", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
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
    const recents = createRecentsStore();
    await recents.registerSession({
      projectId: DEMO_PROJECT_ID,
      sessionId: "sess-1",
      title: "Den chat",
    });

    let pendingDuringPost = 0;
    const client = stubClient({
      sendPrompt: vi.fn().mockImplementation(async (_sid: string, req: { operation_id: string }) => {
        pendingDuringPost = appStore.state.pendingSends["sess-1"]?.length ?? 0;
        applyMessageEvent(appStore, {
          session_id: "sess-1",
          op: "append",
          message: {
            id: req.operation_id,
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content: "Hi",
            created_at: "t",
          },
        });
        applyMessageEvent(appStore, {
          session_id: "sess-1",
          op: "patch",
          message: {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "Hello",
            created_at: "t2",
          },
        });
        return { status: "queued" as const, submission_id: "sub-1", session_revision: 7 };
      }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await sendChatPrompt(
      appStore,
      recents,
      async () => client,
      "sess-1",
      DEMO_PROJECT_ID,
      "/tmp/p",
      emptyProjects, "Hi",
    );

    expect(client.sendPrompt).toHaveBeenCalledWith("sess-1", expect.objectContaining({
      operation_id: expect.any(String),
      text: "Hi",
    }));
    // The store holds exactly the host-echoed rows.
    const sentOperationId = vi.mocked(client.sendPrompt).mock
      .calls[0]?.[1]?.operation_id;
    expect(appStore.state.messages.map((m) => m.id)).toEqual([
      sentOperationId,
      "a1",
    ]);
    expect(appStore.state.messages[0]?.role).toBe("user");
    expect(appStore.state.messages[1]?.content).toBe("Hello");
    expect(pendingDuringPost).toBe(1);
    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
    expect(appStore.state.sessionActivity["sess-1"]?.llmTurn).toBeUndefined();
    expect(client.getSession).toHaveBeenCalledWith("sess-1");
    // The follow-up read reports idle; the prompt stays in flight until a session event after admission.
    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toEqual([{ id: sentOperationId, revision: 7 }]);
    appStore.actions.releasePromptSubmissionsThrough("sess-1", 7);
    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toHaveLength(1);
    appStore.actions.releasePromptSubmissionsThrough("sess-1", 8);
    expect(appStore.state.sessionActivity["sess-1"]).toBeUndefined();
    expect(client.listSessionMessages).not.toHaveBeenCalled();
    expect(recents.state.recents[0]?.sessionId).toBe("sess-1");
    expect(recents.state.recents[0]?.lastActivityAt).toBeTypeOf("number");
  });

  it("sendChatPrompt commits the pending send before any network wait", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
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
    const recents = createRecentsStore();
    const client = stubClient({
      sendPrompt: vi
        .fn()
        .mockResolvedValue({ status: "queued" as const, submission_id: "sub-1", session_revision: 7 }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    let observed: { count: number; posted: boolean; operationId: string } | null =
      null;
    await sendChatPrompt(
      appStore,
      recents,
      async () => client,
      "sess-1",
      DEMO_PROJECT_ID,
      "/tmp/p",
      emptyProjects,
      "Hi",
      undefined,
      undefined,
      undefined,
      {
        onPendingSend: (destination) => {
          expect(destination).toBe("transcript");
          const operationId = appStore.state.pendingSends["sess-1"]![0]!.operationId;
          observed = {
            count: appStore.state.pendingSends["sess-1"]?.length ?? 0,
            posted: vi.mocked(client.sendPrompt).mock.calls.length > 0,
            operationId,
          };
        },
        attachmentLabels: ["notes.txt"],
      },
    );

    expect(observed).toEqual({
      count: 1,
      posted: false,
      operationId: expect.any(String),
    });
    expect(client.sendPrompt).toHaveBeenCalledWith(
      "sess-1",
      expect.objectContaining({ operation_id: observed!.operationId }),
    );
    expect(
      appStore.state.pendingSends["sess-1"]?.[0]?.attachmentLabels,
    ).toEqual(["notes.txt"]);
  });

  it("a prompt sent while a turn runs enters the queue before admission", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const recents = createRecentsStore();
    let queueEntriesDuringPost = -1;
    const client = stubClient({
      sendPrompt: vi.fn().mockImplementation(async () => {
        queueEntriesDuringPost = appStore.state.pendingSends["sess-1"]?.filter((entry) => entry.kind === "queued_prompt").length ?? 0;
        return { status: "queued" as const, submission_id: "sub-1", session_revision: 7 };
      }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    let destination: string | undefined;
    await sendChatPrompt(
      appStore,
      recents,
      async () => client,
      "sess-1",
      DEMO_PROJECT_ID,
      "/tmp/p",
      emptyProjects,
      "Hi",
      undefined,
      undefined,
      undefined,
      { onPendingSend: (surface) => { destination = surface; } },
    );

    expect(destination).toBe("queue");
    expect(queueEntriesDuringPost).toBe(1);
    expect(appStore.state.pendingSends["sess-1"]).toEqual([
      expect.objectContaining({
        kind: "queued_prompt",
        operationId: vi.mocked(client.sendPrompt).mock.calls[0]?.[1].operation_id,
        text: "Hi",
        state: "accepted",
      }),
    ]);
  });

  it("sendChatPrompt holds the submission in flight during POST", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
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
    const recents = createRecentsStore();
    let inFlightDuringPost = false;

    const client = stubClient({
      sendPrompt: vi.fn().mockImplementation(async () => {
        inFlightDuringPost =
          (appStore.state.sessionActivity["sess-1"]?.promptSubmissions?.length ?? 0) === 1;
        return { status: "queued" as const, submission_id: "sub-1", session_revision: 7 };
      }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await sendChatPrompt(
      appStore,
      recents,
      async () => client,
      "sess-1",
      DEMO_PROJECT_ID,
      "/tmp/p",
      emptyProjects, "Hi",
    );

    expect(inFlightDuringPost).toBe(true);
    // An idle read can predate the turn the host is starting.
    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toHaveLength(1);
  });

  it("sendChatPrompt keeps the submission in flight while the session is busy", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
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
    const recents = createRecentsStore();
    const client = stubClient({
      sendPrompt: vi.fn().mockResolvedValue({ status: "queued" as const, submission_id: "sub-1", session_revision: 7 }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await sendChatPrompt(
      appStore,
      recents,
      async () => client,
      "sess-1",
      DEMO_PROJECT_ID,
      "/tmp/p",
      emptyProjects,
      "Hi",
    );

    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toHaveLength(1);
    expect(appStore.state.currentSession?.status).toBe("busy");
    expect(appStore.state.pendingSends["sess-1"]?.map((e) => e.state)).toEqual([
      "accepted",
    ]);
  });

  it("surfaces async prompt host errors from session SSE", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const recents = createRecentsStore();
    const client = stubClient({
      sendPrompt: vi.fn().mockResolvedValue({ status: "queued" as const, submission_id: "sub-1", session_revision: 7 }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await sendChatPrompt(appStore, recents, async () => client, "sess-1", DEMO_PROJECT_ID, "/tmp/p", emptyProjects, "Hi");

    appStore.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      host_error: {
        code: "provider_empty_completion",
        title: "Model returned no response",
        message: "llama-3.1-8b finished without any text or tool calls.",
      },
    });

    // A merged host error is state, not ordering: only a later revision ends the hold.
    const operationId = vi.mocked(client.sendPrompt).mock.calls[0]?.[1]?.operation_id;
    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toEqual([{ id: operationId, revision: 7 }]);
    appStore.actions.releasePromptSubmissionsThrough("sess-1", 8);
    expect(appStore.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("releases the submission when accept POST fails", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const recents = createRecentsStore();
    const client = stubClient({
      sendPrompt: vi.fn().mockRejectedValue(new Error("invalid_request")),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "busy",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await expect(
      sendChatPrompt(appStore, recents, async () => client, "sess-1", DEMO_PROJECT_ID, "/tmp/p", emptyProjects, "Hi"),
    ).rejects.toThrow("invalid_request");

    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toBeUndefined();
    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
  });

  it("surfaces a replayed failed submission as an error, not success", async () => {
    // A terminal receipt remains terminal when replayed.
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
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
    const recents = createRecentsStore();
    const client = stubClient({
      sendPrompt: vi.fn().mockResolvedValue({
        status: "failed" as const,
        submission_id: "sub-1",
        session_revision: 7,
      }),
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await expect(
      sendChatPrompt(appStore, recents, async () => client, "sess-1", DEMO_PROJECT_ID, "/tmp/p", emptyProjects, "Hi"),
    ).rejects.toThrow(/already ran and failed/);

    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toBeUndefined();
    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
  });

  it("llm SSE active drives thinking via store", () => {
    const appStore = createAppStore();
    appStore.actions.setLLMCallStatus({
      call_id: "call-1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    expect(appStore.state.sessionActivity["sess-1"]?.llmTurn?.status).toBe(
      "active",
    );
    appStore.actions.setLLMCallStatus({
      call_id: "call-1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "ok",
      tokens: {},
    });
    expect(appStore.state.sessionActivity["sess-1"]?.llmTurn?.status).toBe(
      "done",
    );
  });

  it("session switch clears transcript via resetChatForSessionSwitch", async () => {
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline(
      "sess-old",
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "old", created_at: "t" }],
      0,
    );
    appStore.actions.resetChatForSessionSwitch();
    expect(appStore.state.messages).toEqual([]);
    expect(appStore.state.currentSession).toBeUndefined();
  });

  it("refreshCreatedSessionBoard leaves bootstrap workflow projection intact", async () => {
    const appStore = createAppStore();
    const projectDir = "/Users/me/proj";
    appStore.actions.setCurrentSession({
      id: "sess-new",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-uuid",
      workspace_path: projectDir,
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });

    const client = stubClient({
      listProjects: vi.fn().mockResolvedValue([
        wireProject(projectDir, "proj-uuid"),
      ]),
      getBoard: vi.fn().mockResolvedValue({ columns: [] }),
      listWorkers: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue({
        id: "run-1",
        session_id: "sess-new",
        workflow_id: "implement",
        workflow_version: "1.0.0",
        attach_policy: "session_create",
        status: "running",
        current_phase: "boot",
        created_at: "t",
        updated_at: "t",
      }),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([
        { id: "plan", version: "1.0.0", name: "Plan", trigger: "/plan" },
      ]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    const projects = testProjectsStore([wireProject(projectDir, "proj-uuid")]);
    await refreshCreatedSessionBoard(
      appStore,
      client,
      projects,
      projectDir,
      "sess-new",
    );

    expect(client.getBoard).toHaveBeenCalledWith("proj-uuid", "sess-new");
    expect(client.getActiveWorkflowRun).not.toHaveBeenCalled();
    expect(client.listWorkflows).not.toHaveBeenCalled();
    expect(client.listBlueprints).not.toHaveBeenCalled();
    expect(appStore.state.workflowCatalog).toHaveLength(0);
  });
});
