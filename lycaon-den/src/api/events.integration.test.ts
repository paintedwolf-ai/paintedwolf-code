import { describe, expect, it, vi } from "vitest";
import { subscribeEvents } from "./events.ts";
import { createAppStore } from "../store/app-state.ts";

describe("subscribeEvents integration", () => {
  it("worker topic updates store and sets reconnecting on drop", async () => {
    const appStore = createAppStore();
    let emit: ((chunk: { data?: string; comment?: string }) => void) | undefined;

    async function* mockConnect() {
      yield { comment: "connected" };
      while (true) {
        const chunk = await new Promise<{ data?: string; comment?: string }>(
          (resolve) => {
            emit = resolve;
          },
        );
        yield chunk;
        if (chunk.data === "END") return;
      }
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" },
      "proj-1",
      {},
      {
        storeActions: appStore.actions,
        connect: () => mockConnect(),
        onReconnectAttempt: () => appStore.actions.setSidecarStatus("reconnecting"),
      },
    );

    await vi.waitFor(() => expect(emit).toBeDefined());

    appStore.actions.setWorkers([
      {
        id: "task-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);

    emit!({
      data: JSON.stringify({
        v: 1,
        event_id: "11111111-1111-4111-8111-111111111111",
        cursor: "cursor-1",
        topic: "worker",
        published_at: "2026-01-01T00:00:00Z",
        scope: { kind: "project", project_id: "proj-1" },
        data: { worker_id: "task-1", status: "complete" },
      }),
    });

    await vi.waitFor(() => {
      expect(appStore.state.workers[0]?.status).toBe("complete");
    });

    void sub.close();
  });
});
