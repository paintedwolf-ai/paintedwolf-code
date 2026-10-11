import { bindAppConnection } from "../../platform/connection/app-connection.ts";
import { connectionServicesFixture } from "../../test/connection-services-fixture.ts";
import { resetSessionSnapshotReads } from "../../api/session-snapshot-refresh.ts";
import { stubClient, type ClientStubs } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { selectSessionNotices } from "../../notices/notice-select.ts";
import { sendChatPrompt } from "./session-lifecycle.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { Session } from "../../api/types.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { deferred, stalledRecents } from "./session-lifecycle-test-fixture.ts";

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

  it("sends recovery identity without expanding its visible text", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(idle);
    const client = promptClient();
    const recovery = { action: "continue" as const, after_message_id: "last-progress" };
    await sendChatPrompt(appStore, stalledRecents(), async () => client,
      "sess-1", "proj-1", "/tmp/p", emptyProjects, "Keep going",
      undefined, undefined, undefined, { recovery });
    expect(client.sendPrompt).toHaveBeenCalledWith("sess-1", expect.objectContaining({
      text: "Keep going", recovery,
    }));
  });

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
    bindAppConnection(connectionServicesFixture({ notices: notices }));
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
