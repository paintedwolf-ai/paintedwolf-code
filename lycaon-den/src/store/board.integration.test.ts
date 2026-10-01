import { describe, expect, it, vi } from "vitest";
import { subscribeEvents } from "../api/events.ts";
import { createAppStore } from "./app-state.ts";
import type { BoardView, EventEnvelope } from "../api/types.ts";

const boardView = (line: string): BoardView =>
  ({
    project_id: "proj-1",
    pack_content_hash: line,
  }) as unknown as BoardView;

describe("board SSE integration", () => {
  it("debounced board topic updates store snapshot", async () => {
    vi.useFakeTimers();
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
      },
    );

    await vi.waitFor(() => expect(emit).toBeDefined());

    const payload = (line: string) =>
      JSON.stringify({
        v: 1,
        event_id: crypto.randomUUID(),
        cursor: crypto.randomUUID(),
        topic: "board",
        published_at: "2025-01-01T00:00:00Z",
        scope: { kind: "project", project_id: "proj-1" },
        data: { project_id: "proj-1", detail_level: "compact", snapshot: boardView(line) },
      } satisfies EventEnvelope);

    emit!({ data: payload("live") });
    await vi.waitFor(() => expect(appStore.state.board).toBeUndefined());

    await vi.advanceTimersByTimeAsync(200);
    expect(appStore.state.board?.pack_content_hash).toBe("live");

    void sub.close();
    vi.useRealTimers();
  });
});
