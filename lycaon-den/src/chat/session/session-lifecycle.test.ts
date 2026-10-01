import { resetSessionSnapshotReads } from "../../api/session-snapshot-refresh.ts";
import { stubClient, type ClientStubs } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import {
  registerNoticeStore,
  registerProjectsStore,
} from "../../platform/connection/app-connection.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { selectSessionNotices } from "../../notices/notice-select.ts";
import { createRecentsStore, type RecentsStore } from "../../store/recents-store.ts";
import {
  refreshCreatedSessionBoard,
  recordCreatedSessionInRecents,
  resumeChatSession,
  sendChatPrompt,
  stopChatActivity,
} from "./session-lifecycle.ts";
import {
  clearSessionChatCacheForTests,
  getSessionChatCache,
  putSessionChatCache,
} from "./session-chat-cache.ts";
import { buildChatTranscriptBlocks } from "../workflow/workflow-spans.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { Session } from "../../api/types.ts";
import { DEFAULT_SESSION_TITLE } from "../../../shared/app-state-types.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";

function testProjectsStore(seed: ReturnType<typeof wireProject>[] = []) {
  const store = createProjectsStore(() => null, {
    onProjectEvent: () => () => {},
  });
  if (seed.length > 0) store.load(seed);
  return store;
}

function withBootstrap(
  client: ClientStubs & {
    getSession: (id: string) => Promise<unknown>;
    listSessionMessages: (id: string) => Promise<unknown>;
  },
): LycaonClient {
  return stubClient(Object.assign(client, {
    getSessionBootstrap: vi.fn(async (id: string) => {
      const [session, transcript] = await Promise.all([
        client.getSession(id),
        client.listSessionMessages(id),
      ]);
      return {
        event_cursor: "",
        revision: (transcript as { watermark?: number }).watermark ?? 0,
        session,
        transcript,
        progress: { steps: [], revision: 0 },
        turn_clock: { session_id: id, active_ms: 0, work_ms: 0, running: false },
        findings: { findings: [], revision: 0 },
        queue: { queue_items: [], hold: false, sending: false, revision: 0 },
        coordinator: {},
        background_outputs: [],
        previews: [],
        workers: [],
        checkpoints: [],
      };
    }),
  }));
}

describe("session-lifecycle", () => {
  it("refreshCreatedSessionBoard refreshes project and board without repeating bootstrap workflow reads", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-new", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "proj-1", workspace_path: "/tmp/p",
      posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    });
    const projects = testProjectsStore([wireProject("/tmp/p")]);
    const listWorkflows = vi.fn().mockResolvedValue([]);
    const listRuns = vi.fn().mockResolvedValue({ runs: [] });
    const listBlueprints = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      listProjects: vi.fn().mockResolvedValue([
        wireProject("/tmp/p"),
      ]),
      getBoard: vi.fn().mockResolvedValue({ columns: [] }),
      listWorkers: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue({
        id: "run-1",
        session_id: "sess-new",
        workflow_id: "implement",
        status: "running",
        current_phase: "boot",
        attach_policy: "session_create",
        created_at: "t",
        updated_at: "t",
      }),
      listSessionWorkflowRuns: listRuns,
      listWorkflows,
      listBlueprints,
    });

    await refreshCreatedSessionBoard(appStore, client, projects, "/tmp/p", "sess-new");

    expect(client.listProjects).toHaveBeenCalledTimes(1);
    expect(client.getBoard).toHaveBeenCalledWith("proj-1", "sess-new");
    expect(client.getActiveWorkflowRun).not.toHaveBeenCalled();
    expect(listWorkflows).not.toHaveBeenCalled();
    expect(listRuns).not.toHaveBeenCalled();
    expect(listBlueprints).not.toHaveBeenCalled();
  });

  it("recordCreatedSessionInRecents lists the new session first under its project", async () => {
    const recents = createRecentsStore();
    const projectId = "proj-1";
    for (let i = 0; i < 5; i++) {
      await recents.registerSession({
        projectId,
        sessionId: `sess-old-${i}`,
        title: `Old ${i}`,
      });
    }

    await recordCreatedSessionInRecents(recents, {
      projectId,
      sessionId: "sess-new",
    });

    const projectRecents = recents.state.recents.filter(
      (row) => row.projectId === projectId,
    );
    expect(projectRecents[0]?.sessionId).toBe("sess-new");
    expect(projectRecents[0]?.title).toBe(DEFAULT_SESSION_TITLE);
  });

  it("resumeChatSession hydrates runs referenced on transcript rows", async () => {
    const appStore = createAppStore();
    const terminalRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create" as const,
      status: "complete" as const,
      current_phase: "boot",
      created_at: "t",
      updated_at: "t",
    };
    const messages = [
      {
        id: "m1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        workflow_run_id: "run-1",
        created_at: "t",
      },
    ];
    const client = withBootstrap({
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
      listSessionMessages: vi.fn().mockResolvedValue({ messages, watermark: 0, turn_clocks: {}, turn_loads: {} }),
      listCheckpoints: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      getWorkflowRun: vi.fn().mockResolvedValue(terminalRun),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await resumeChatSession(appStore, client, "sess-1", emptyProjects);

    expect(client.getWorkflowRun).toHaveBeenCalledWith("run-1");
    expect(appStore.state.workflowRuns).toEqual([terminalRun]);
    expect(() =>
      buildChatTranscriptBlocks(
        appStore.state.messages,
        appStore.state.workflowRuns,
        appStore.state.activeWorkflowRun,
      ),
    ).not.toThrow();
  });

  it("resumeChatSession installs workflow state before messages without redundant checkpoint IO", async () => {
    const appStore = createAppStore();
    const ambientRun = {
      id: "run-ambient",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "boot",
      created_at: "t",
      updated_at: "t",
    };
    const messages = [
      {
        id: "m1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary" as const,
        visibility: "internal" as const,
        workflow_run_id: "run-ambient",
        created_at: "t",
      },
    ];
    const callOrder: string[] = [];
    const client = withBootstrap({
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
      listSessionMessages: vi
        .fn()
        .mockResolvedValue({ messages, watermark: 0, turn_clocks: {}, turn_loads: {} }),
      listCheckpoints: vi.fn().mockImplementation(async () => {
        callOrder.push("checkpoints");
        return [];
      }),
      getActiveWorkflowRun: vi.fn().mockImplementation(async () => {
        callOrder.push("activeRun");
        return ambientRun;
      }),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [ambientRun] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });
    const originalInstall = appStore.actions.installTranscriptBaseline.bind(
      appStore.actions,
    );
    const installSpy = vi.spyOn(appStore.actions, "installTranscriptBaseline");
    installSpy.mockImplementation((sessionId, next, watermark) => {
      callOrder.push("installBaseline");
      expect(() =>
        buildChatTranscriptBlocks(
          next,
          appStore.state.workflowRuns,
          appStore.state.activeWorkflowRun,
        ),
      ).not.toThrow();
      originalInstall(sessionId, next, watermark);
    });

    await resumeChatSession(appStore, client, "sess-1", emptyProjects);

    expect(callOrder.indexOf("activeRun")).toBeLessThan(
      callOrder.indexOf("installBaseline"),
    );
    expect(client.listCheckpoints).not.toHaveBeenCalled();
    installSpy.mockRestore();
  });

  it("resumeChatSession opens a session whose workflow runs are missing", async () => {
    const appStore = createAppStore();
    const messages = [
      {
        id: "m1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        created_at: "t",
      },
    ];
    const client = withBootstrap({
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
      listSessionMessages: vi.fn().mockResolvedValue({ messages, watermark: 0, turn_clocks: {}, turn_loads: {} }),
      listCheckpoints: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    // A missing run is an enrichment gap; the rows still open.
    await resumeChatSession(appStore, client, "sess-1", emptyProjects);

    expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1"]);
    const blocks = buildChatTranscriptBlocks(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
    expect(blocks.flatMap((b) => b.items)).not.toHaveLength(0);
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

/** Recents whose disk write never settles, as under the persist debounce. */
function stalledRecents(): RecentsStore {
  const never = () => new Promise<void>(() => undefined);
  return {
    registerSession: vi.fn(never),
    recordSessionActivity: vi.fn(never),
  } as unknown as RecentsStore;
}

describe("sendChatPrompt", () => {
  const idle = {
    id: "sess-1",
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "proj-1",
    workspace_path: "/tmp/p",
    posture: "build" as const,
    status: "idle" as const,
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  };

  function promptClient(overrides: ClientStubs = {}) {
    return stubClient({
      sendPrompt: vi.fn().mockResolvedValue({ status: "queued", submission_id: "sub-1", session_revision: 7 }),
      getSession: vi.fn().mockResolvedValue(idle),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
      ...overrides,
    });
  }

  it("seats the prompt and releases the draft before pre-send work resolves", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const client = promptClient();
    const prepared = deferred<LycaonClient>();
    const onPendingSend = vi.fn();

    const sending = sendChatPrompt(
      appStore, stalledRecents(), () => prepared.promise,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi",
      undefined, undefined, undefined, { onPendingSend },
    );

    expect(appStore.state.pendingSends["sess-1"]).toHaveLength(1);
    expect(onPendingSend).toHaveBeenCalledWith("transcript");
    expect(client.sendPrompt).not.toHaveBeenCalled();

    prepared.resolve(client);
    await sending;
    expect(client.sendPrompt).toHaveBeenCalledOnce();
  });

  it.each(["busy", "held", "starting", "submitting"] as const)(
    "puts a prompt directly in the queue while %s, before preparation finishes",
    async (reason) => {
      const appStore = createAppStore();
      appStore.actions.setCurrentSession({ ...idle, status: reason === "busy" ? "busy" : "idle",
        prompt_pending: reason === "starting" });
      if (reason === "held") appStore.actions.setQueueDraft("sess-1", appStore.state.sessionViewEpoch,
        { queue_items: [], hold: true, sending: false, revision: 1 });
      if (reason === "submitting") appStore.actions.addPendingSend("sess-1",
        { kind: "prompt", operationId: "earlier", text: "first", state: "sending" });
      const prepared = deferred<LycaonClient>();
      const onPendingSend = vi.fn();
      const sending = sendChatPrompt(appStore, stalledRecents(), () => prepared.promise,
        "sess-1", "proj-1", "/tmp/p", emptyProjects, "Next",
        undefined, undefined, undefined, { onPendingSend });
      expect(appStore.state.pendingSends["sess-1"]?.at(-1)).toMatchObject({
        kind: "queued_prompt", text: "Next", state: "sending",
      });
      expect(onPendingSend).toHaveBeenCalledWith("queue");
      prepared.resolve(promptClient());
      await sending;
      expect(appStore.state.pendingSends["sess-1"]?.at(-1)).toMatchObject({
        kind: "queued_prompt", state: "accepted",
      });
    },
  );

  it("removes a pending queue entry when submission fails", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ ...idle, status: "busy" });
    const prepared = deferred<LycaonClient>();
    const sending = sendChatPrompt(appStore, stalledRecents(), () => prepared.promise,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Next");
    expect(appStore.state.pendingSends["sess-1"]?.[0]?.kind).toBe("queued_prompt");
    prepared.resolve(promptClient({ sendPrompt: vi.fn().mockRejectedValue(new Error("Admission failed")) }));
    await expect(sending).rejects.toThrow("Admission failed");
    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
  });

  it("allows preparation to establish the initial backend before sending", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const client = promptClient();
    await sendChatPrompt(appStore, stalledRecents(), async () => {
      resetSessionSnapshotReads(appStore.actions);
      appStore.actions.invalidateSessionViewRequests();
      return client;
    }, "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi",
    undefined, undefined, undefined, { shouldSend: (prepared) => prepared === client });
    expect(client.sendPrompt).toHaveBeenCalledOnce();
  });

  it("rejects a prepared client superseded before prompt admission", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const client = promptClient();
    const prepared = deferred<LycaonClient>();
    let current = true;
    const sending = sendChatPrompt(appStore, stalledRecents(), () => prepared.promise,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi",
      undefined, undefined, undefined, { shouldSend: () => current });
    current = false;
    prepared.resolve(client);
    await expect(sending).rejects.toMatchObject({ name: "AbortError" });
    expect(client.sendPrompt).not.toHaveBeenCalled();
    expect(client.getSession).not.toHaveBeenCalled();
    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
  });

  it.each(["success", "failure"] as const)("does not continue an old backend prompt after %s", async (outcome) => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const accepted = deferred<Awaited<ReturnType<LycaonClient["sendPrompt"]>>>();
    const client = promptClient({ sendPrompt: vi.fn(() => accepted.promise) });
    const sending = sendChatPrompt(appStore, stalledRecents(), async () => client,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi");
    await vi.waitFor(() => expect(client.sendPrompt).toHaveBeenCalledOnce());
    resetSessionSnapshotReads(appStore.actions);
    appStore.actions.holdPromptSubmission("sess-1", "newer-backend-prompt");
    if (outcome === "failure") {
      accepted.reject(new Error("old send failed"));
      await expect(sending).rejects.toThrow("old send failed");
    } else {
      accepted.resolve({ status: "queued", operation_id: "sub-1", message_id: "m-1", session_revision: 7 });
      await sending;
    }
    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toContainEqual({ id: "newer-backend-prompt" });
    expect(client.getSession).not.toHaveBeenCalled();
    expect(client.getActiveWorkflowRun).not.toHaveBeenCalled();
  });

  it("keeps newer host activity when an earlier prompt request fails", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const accepted = deferred<Awaited<ReturnType<LycaonClient["sendPrompt"]>>>();
    const client = promptClient({
      sendPrompt: vi.fn(() => accepted.promise),
      getSession: vi.fn(() => new Promise<Session>(() => undefined)),
    });
    const sending = sendChatPrompt(appStore, stalledRecents(), async () => client,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi");
    await vi.waitFor(() => expect(client.sendPrompt).toHaveBeenCalledOnce());
    appStore.actions.setLLMCallStatus({
      session_id: "sess-1", call_id: "new-call", status: "active",
      provider: "mock", model: "mock", tokens: {},
    });
    accepted.reject(new Error("old prompt request failed"));
    await expect(sending).rejects.toThrow("old prompt request failed");
    // The failed request releases only its own submission.
    expect(appStore.state.sessionActivity["sess-1"]?.llmTurn).toMatchObject({ callId: "new-call", status: "active" });
    expect(appStore.state.sessionActivity["sess-1"]?.promptSubmissions).toBeUndefined();
    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
  });

  it("retains accepted background sends after navigation on the same backend", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const accepted = deferred<Awaited<ReturnType<LycaonClient["sendPrompt"]>>>();
    const client = promptClient({ sendPrompt: vi.fn(() => accepted.promise) });
    const sending = sendChatPrompt(appStore, stalledRecents(), async () => client,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi");
    await vi.waitFor(() => expect(client.sendPrompt).toHaveBeenCalledOnce());
    appStore.actions.setCurrentSession({ ...idle, id: "sess-2" });
    accepted.resolve({ status: "queued", operation_id: "sub-1", message_id: "m-1", session_revision: 7 });
    await sending;
    expect(appStore.state.pendingSends["sess-1"]?.[0]?.state).toBe("accepted");
    expect(appStore.state.currentSession?.id).toBe("sess-2");
  });

  it("retracts the seat and reports when pre-send work fails", async () => {
    const notices = createNoticeStore();
    registerNoticeStore(notices);
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);

    await expect(
      sendChatPrompt(
        appStore, stalledRecents(), () => Promise.reject(new Error("offline")),
        "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi",
      ),
    ).rejects.toThrow("offline");

    expect(appStore.state.pendingSends["sess-1"]).toBeUndefined();
    expect(selectSessionNotices(notices.index(), "sess-1")).toHaveLength(1);
  });

  it("settles on host acceptance without waiting for recents or the status refresh", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const client = promptClient({
      getSession: vi.fn(() => new Promise<Session>(() => undefined)),
    });
    const recents = stalledRecents();

    await sendChatPrompt(
      appStore, recents, async () => client,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Hi",
    );

    expect(client.sendPrompt).toHaveBeenCalledOnce();
    expect(recents.recordSessionActivity).toHaveBeenCalledWith({
      projectId: "proj-1",
      sessionId: "sess-1",
    });
    expect(client.getSession).toHaveBeenCalledWith("sess-1");
  });
});

describe("resumeChatSession recents and cancellation", () => {
  it("applies the transcript without waiting for the recents write and passes the switch signal", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    const recents = stalledRecents();
    const client = withBootstrap({
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        title: "Named chat",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi.fn().mockResolvedValue({
        messages: [
          {
            id: "m1",
            role: "user", origin: "user", authority: "user", trust_tier: "trusted",
            content: "hello",
            created_at: "t",
          },
        ],
        watermark: 0,
        turn_clocks: {},
        turn_loads: {},
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });
    const signal = new AbortController().signal;

    await resumeChatSession(appStore, client, "sess-1", emptyProjects, recents, {
      projectId: "proj-1",
      signal,
    });

    expect(recents.registerSession).toHaveBeenCalledWith({
      projectId: "proj-1",
      sessionId: "sess-1",
      title: "Named chat",
    });
    expect(client.getSessionBootstrap).toHaveBeenCalledWith("sess-1", { signal });
    expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1"]);
  });
});

describe("stopChatActivity", () => {
  const idleSession = (id: string, projectDir: string) => ({
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "proj-1",
    workspace_path: projectDir,
    status: "idle" as const,
    posture: "build" as const,
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  });
  const busySession = (id: string, projectDir: string) => ({
    ...idleSession(id, projectDir),
    status: "busy" as const,
  });

  it("does not start old-backend follow-ups or clear a replacement stop attempt", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    let answer!: (session: Session) => void;
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({ abortSession: () => new Promise<Session>((resolve) => { answer = resolve; }), listWorkers });
    const stopping = stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    resetSessionSnapshotReads(appStore.actions);
    appStore.actions.setSessionStopping("s1", true);
    answer(idleSession("s1", "/tmp/p"));
    await stopping;
    expect(listWorkers).not.toHaveBeenCalled();
    expect(appStore.state.sessionActivity["s1"]?.stopping).toBe(true);
    expect(appStore.state.currentSession?.status).toBe("busy");
  });

  it("preserves a reopened session while finishing an earlier abort", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    let resolveAbort!: (session: Session) => void;
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      abortSession: () => new Promise<Session>((resolve) => { resolveAbort = resolve; }),
      listWorkers,
    });
    const aborting = stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    appStore.actions.setCurrentSession(busySession("s2", "/tmp/p"));
    appStore.actions.setCurrentSession({ ...busySession("s1", "/tmp/p"), title: "Reopened" });
    resolveAbort(idleSession("s1", "/tmp/p"));
    await aborting;
    expect(appStore.state.currentSession).toMatchObject({ status: "busy", title: "Reopened" });
    expect(listWorkers).toHaveBeenCalledOnce();
    expect(appStore.state.sessionActivity["s1"]).toBeUndefined();
  });

  it("posts abort, merges returned session, and clears activity flags", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    appStore.actions.holdPromptSubmission("s1", "submission-1");

    const abort = vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p"));
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({ abortSession: abort, listWorkers });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
    expect(appStore.state.currentSession?.status).toBe("idle");
    expect(appStore.state.sessionActivity["s1"]).toBeUndefined();
  });

  it("does not merge the returned session when the foreground has switched", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("other", "/tmp/p"));

    const abort = vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p"));
    const client = stubClient({
      abortSession: abort,
      listWorkers: vi.fn().mockResolvedValue([]),
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
    // Superseded hydration leaves the current session intact.
    expect(appStore.state.currentSession?.id).toBe("other");
    expect(appStore.state.currentSession?.status).toBe("busy");
  });

  it("with null client preserves truthful local activity", async () => {
    const appStore = createAppStore();
    appStore.actions.holdPromptSubmission("s1", "submission-1");
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    await stopChatActivity(appStore, null, "s1", "p1", "/tmp/p", emptyProjects);

    expect(appStore.state.sessionActivity["s1"]?.promptSubmissions).toEqual([{ id: "submission-1" }]);
    expect(appStore.state.sessionActivity["s1"]?.llmTurn?.status).toBe("active");
  });

  it("surfaces abort errors and restores the live stop state", async () => {
    const appStore = createAppStore();
    const notices = createNoticeStore();
    registerNoticeStore(notices);
    appStore.actions.holdPromptSubmission("s1", "submission-1");

    const abort = vi.fn().mockRejectedValue(new Error("network down"));
    const client = stubClient({
      abortSession: abort,
      listWorkers: vi.fn().mockResolvedValue([]),
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
    // Scoped to the chat that failed to stop, not to whichever chat is in front.
    expect(selectSessionNotices(notices.index(), "s1")).toHaveLength(1);
    expect(appStore.state.sessionActivity["s1"]?.promptSubmissions).toEqual([{ id: "submission-1" }]);
    expect(appStore.state.sessionActivity["s1"]?.stopping).toBeUndefined();
  });

  it("refreshes session workers after a successful abort", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(busySession("s1", "/tmp/p"));
    const projectRows = [wireProject("/tmp/p", "proj-1")];
    registerProjectsStore(testProjectsStore(projectRows));

    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      abortSession: vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p")),
      listWorkers,
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", projectRows);

    expect(listWorkers).toHaveBeenCalledWith("proj-1", { sessionId: "s1" });
  });

  it("a disconnected stop does not clear llm turn chrome", async () => {
    const appStore = createAppStore();
    appStore.actions.setLLMCallStatus({
      call_id: "msg-1",
      session_id: "s1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    await stopChatActivity(appStore, null, "s1", "p1", "/tmp/p", emptyProjects);

    expect(appStore.state.sessionActivity["s1"]?.llmTurn?.status).toBe("active");
  });

  it("uses the single host abort operation for active catalog workflows", async () => {
    const appStore = createAppStore();
    registerProjectsStore(testProjectsStore([wireProject("/tmp/p", "proj-1")]));
    appStore.actions.setCurrentSession(idleSession("s1", "/tmp/p"));
    appStore.actions.setWorkflowState("s1", appStore.state.sessionViewEpoch, {
      activeWorkflowRun: {
        id: "run-plan",
        session_id: "s1",
        workflow_id: "plan",
        workflow_version: "1.0.0",
		revision: 1,
        status: "running",
        current_phase: "plan",
        created_at: "t",
        updated_at: "t",
      },
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
    });

    const abort = vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p"));
    const cancelWorkflowRun = vi.fn().mockResolvedValue({
      id: "run-plan",
      session_id: "s1",
      workflow_id: "plan",
      workflow_version: "1.0.0",
	  revision: 1,
      status: "cancelled",
      current_phase: "plan",
      created_at: "t",
      updated_at: "t",
    });
    const client = stubClient({
      abortSession: abort,
      listWorkers: vi.fn().mockResolvedValue([]),
      cancelWorkflowRun,
      getActiveWorkflowRun: vi.fn().mockResolvedValue(undefined),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(abort).toHaveBeenCalledWith("s1");
		expect(cancelWorkflowRun).not.toHaveBeenCalled();
  });

  it("coalesces repeat stop clicks while the host barrier is running", async () => {
    const appStore = createAppStore();
    appStore.actions.holdPromptSubmission("s1", "submission-1");
    let resolveAbort!: (session: Session) => void;
    const abortSession = vi.fn(() => new Promise<Session>((resolve) => {
      resolveAbort = resolve;
    }));
    const client = stubClient({
      abortSession,
      listWorkers: vi.fn().mockResolvedValue([]),
    });

    const first = stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    expect(appStore.state.sessionActivity["s1"]?.stopping).toBe(true);
    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);
    expect(abortSession).toHaveBeenCalledTimes(1);

    resolveAbort(idleSession("s1", "/tmp/p"));
    await expect(first).resolves.toBeUndefined();
    expect(appStore.state.sessionActivity["s1"]).toBeUndefined();
  });

  it("does not cancel the ambient implement workflow on stop", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idleSession("s1", "/tmp/p"));
    appStore.actions.setWorkflowState("s1", appStore.state.sessionViewEpoch, {
      activeWorkflowRun: {
        id: "run-ambient",
        session_id: "s1",
        workflow_id: "implement",
        workflow_version: "1.0.0",
		revision: 1,
        attach_policy: "session_create",
        status: "running",
        current_phase: "work",
        created_at: "t",
        updated_at: "t",
      },
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
    });

    const cancelWorkflowRun = vi.fn();
    const client = stubClient({
      abortSession: vi.fn().mockResolvedValue(idleSession("s1", "/tmp/p")),
      listWorkers: vi.fn().mockResolvedValue([]),
      cancelWorkflowRun,
    });

    await stopChatActivity(appStore, client, "s1", "p1", "/tmp/p", emptyProjects);

    expect(cancelWorkflowRun).not.toHaveBeenCalled();
  });

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

});
