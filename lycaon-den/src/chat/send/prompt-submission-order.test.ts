import { describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { sendChatPrompt, stopChatActivity } from "../session/session-lifecycle.ts";
import { reservePromptSubmission } from "./prompt-submission-order.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function fixture() {
  const store = createAppStore();
  const session = {
    id: "session", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "project", workspace_path: "/tmp/project",
    posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
  } as const;
  store.actions.setCurrentSession(session);
  const client = stubClient({
    sendPrompt: vi.fn<LycaonClient["sendPrompt"]>().mockResolvedValue({ status: "queued", operation_id: "submission", message_id: "m-1", session_revision: 7 }),
    getSession: vi.fn().mockResolvedValue(session),
    abortSession: vi.fn().mockResolvedValue(session),
    getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
    listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
    listWorkflows: vi.fn().mockResolvedValue([]),
    listBlueprints: vi.fn().mockResolvedValue([]),
  });
  const recents = createRecentsStore();
  const send = (text: string, prepare: () => Promise<LycaonClient> = async () => client) => sendChatPrompt(
    store, recents, prepare, session.id, session.project_id, session.workspace_path, [], text,
  );
  return { store, client, send };
}

describe("prompt submission order", () => {
  it.each(["preparation", "acceptance"])("preserves three sends through delayed %s", async (phase) => {
    const { store, client, send } = fixture();
    const gate = deferred<void>();
    const prepare = vi.fn(async () => {
      if (phase === "preparation") await gate.promise;
      return client;
    });
    if (phase === "acceptance") vi.mocked(client.sendPrompt).mockImplementationOnce(async () => {
      await gate.promise;
      return { status: "queued", submission_id: "first", session_revision: 7 };
    });
    const first = send("first", prepare);
    const secondPrepare = vi.fn(async () => client);
    const second = send("second", secondPrepare);
    const third = send("third");
    expect(store.state.pendingSends.session?.map((entry) => entry.text)).toEqual(["first", "second", "third"]);
    await vi.waitFor(() => expect(prepare).toHaveBeenCalledOnce());
    expect(secondPrepare).not.toHaveBeenCalled();
    expect(vi.mocked(client.sendPrompt).mock.calls.map((call) => call[1].text)).not.toContain("second");
    gate.resolve();
    await Promise.all([first, second, third]);
    expect(vi.mocked(client.sendPrompt).mock.calls.map((call) => call[1].text)).toEqual(["first", "second", "third"]);
  });

  it.each(["preparation", "acceptance"])("releases the next send after failed %s", async (phase) => {
    const { client, send } = fixture();
    const failure = new Error("fixture failure");
    const prepare = phase === "preparation"
      ? vi.fn<() => Promise<LycaonClient>>().mockRejectedValue(failure)
      : async () => client;
    if (phase === "acceptance") vi.mocked(client.sendPrompt).mockRejectedValueOnce(failure);
    const first = send("first", prepare).catch((error: unknown) => error);
    const second = send("second");
    expect(await first).toBe(failure);
    await second;
    expect(vi.mocked(client.sendPrompt).mock.calls.slice(-1)[0]?.[1].text).toBe("second");
  });

  it("does not block another window or another session", async () => {
    const { store, client, send } = fixture();
    const gate = deferred<void>();
    const first = send("blocked", async () => { await gate.promise; return client; });
    const otherSession = reservePromptSubmission(store, "other-session");
    await otherSession.ready;
    otherSession.finish();
    const other = fixture();
    await other.send("independent window");
    expect(other.client.sendPrompt).toHaveBeenCalledOnce();
    expect(client.sendPrompt).not.toHaveBeenCalled();
    gate.resolve();
    await first;
  });

  it("Stop cancels prepared and waiting sends without blocking a later send", async () => {
    const { store, client, send } = fixture();
    const gate = deferred<void>();
    const prepare = vi.fn(async () => { await gate.promise; return client; });
    const first = send("first", prepare).catch((error: unknown) => error);
    const second = send("second").catch((error: unknown) => error);
    await vi.waitFor(() => expect(prepare).toHaveBeenCalledOnce());
    await stopChatActivity(store, client, "session", "project", "/tmp/project", []);
    gate.resolve();
    for (const result of await Promise.all([first, second])) {
      expect(result).toBeInstanceOf(DOMException);
      expect((result as DOMException).name).toBe("AbortError");
    }
    expect(client.sendPrompt).not.toHaveBeenCalled();
    expect(store.state.pendingSends.session).toBeUndefined();
    await send("after stop");
    expect(client.sendPrompt).toHaveBeenCalledWith("session", expect.objectContaining({ text: "after stop" }));
  });

  it("does not submit optimistic entries removed while preparation waits", async () => {
    const { store, client, send } = fixture();
    const gate = deferred<void>();
    const first = send("first", async () => { await gate.promise; return client; }).catch((error: unknown) => error);
    const second = send("second").catch((error: unknown) => error);
    store.actions.removePendingSends("session", store.state.pendingSends.session!.map((entry) => entry.operationId));
    gate.resolve();
    await Promise.all([first, second]);
    expect(client.sendPrompt).not.toHaveBeenCalled();
  });
});
