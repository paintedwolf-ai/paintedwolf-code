import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it } from "vitest";
import {
  applyLiveStreamChunkToMessage,
  applyPaintedLiveChunk,
  followSessionLiveStream,
  parseLiveStreamChunk,
  streamingMessageId,
} from "./session-live-stream.ts";
import type { Message } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import type { AppStore } from "../../store/app-state-model.ts";

describe("session-live-stream", () => {
  it("patches only the streaming worker row even when history is large", () => {
    const rows = Array.from({ length: 1000 }, (_, index) => ({
      id: `row-${index}`, role: "assistant", content: "before", status: "streaming",
    } as Message));
    const patches: Message[][] = [];
    const store = {
      state: { workerTranscripts: { worker: { rows } } },
      actions: { applyWorkerTranscriptRows: (_id: string, patch: Message[]) => patches.push(patch) },
    } as unknown as AppStore;
    applyPaintedLiveChunk(store, { sessionId: "child", messageId: "row-999", kind: "worker", workerId: "worker" }, { token: " after" });
    expect(patches).toHaveLength(1);
    expect(patches[0]).toHaveLength(1);
    expect(patches[0]?.[0]?.content).toBe("before after");
    expect(rows[999]?.content).toBe("before");
  });
  it("streamingMessageId picks the trailing streaming assistant", () => {
    const rows = [
      { id: "u1", role: "user", content: "hi", status: "complete" },
      { id: "a1", role: "assistant", content: "old", status: "complete" },
      { id: "a2", role: "assistant", content: "", status: "streaming" },
    ] as Message[];
    expect(streamingMessageId(rows)).toBe("a2");
  });

  it("applyLiveStreamChunkToMessage appends deltas and replaces resets", () => {
    const base = {
      id: "a1",
      role: "assistant",
      content: "hel",
      status: "streaming",
    } as Message;
    expect(applyLiveStreamChunkToMessage(base, { token: "lo" }).content).toBe(
      "hello",
    );
    expect(
      applyLiveStreamChunkToMessage(base, { token: "complete", reset: true })
        .content,
    ).toBe("complete");
    expect(
      applyLiveStreamChunkToMessage(base, { token: "", reset: true }).content,
    ).toBe("");
    expect(applyLiveStreamChunkToMessage(base, { done: true }).status).toBe(
      "complete",
    );
  });

  it("applyPaintedLiveChunk ignores a chunk once the row has settled", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "final answer",
          status: "complete",
          created_at: "t",
          seq: 5,
          tool_calls: [
            { id: "tc1", name: "read", args: {} },
            { id: "tc2", name: "command", args: {} },
          ],
        },
      ],
      5,
    );
    applyPaintedLiveChunk(
      store,
      { sessionId: "sess-1", messageId: "a1", kind: "parent" },
      { token: "stale", tool_calls: [{ id: "tc2", name: "command", args: {} }] },
    );
    const row = store.state.messages[0];
    expect(row?.content).toBe("final answer");
    expect(row?.tool_calls).toHaveLength(2);
    expect(row?.status).toBe("complete");
  });

  it("applyPaintedLiveChunk applies a chunk while the row is still streaming", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline(
      "sess-1",
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "partial",
          status: "streaming",
          created_at: "t",
          seq: 5,
        },
      ],
      5,
    );
    applyPaintedLiveChunk(
      store,
      { sessionId: "sess-1", messageId: "a1", kind: "parent" },
      { token: " more", tool_calls: [{ id: "tc1", name: "read", args: {} }] },
    );
    const row = store.state.messages[0];
    expect(row?.content).toBe("partial more");
    expect(row?.tool_calls).toHaveLength(1);
  });

  it("applyPaintedLiveChunk ignores a chunk once a worker row has settled", () => {
    const store = createAppStore();
    store.actions.applyWorkerTranscriptRows("w1", [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "final answer",
        status: "complete",
        created_at: "t",
        tool_calls: [
          { id: "tc1", name: "read", args: {} },
          { id: "tc2", name: "command", args: {} },
        ],
      },
    ]);
    applyPaintedLiveChunk(
      store,
      {
        sessionId: "child-1",
        messageId: "a1",
        kind: "worker",
        workerId: "w1",
      },
      { token: "stale", tool_calls: [{ id: "tc2", name: "command", args: {} }] },
    );
    const row = store.state.workerTranscripts["w1"]?.rows?.[0];
    expect(row?.content).toBe("final answer");
    expect(row?.tool_calls).toHaveLength(2);
  });

  it("discards buffered parent text after the transcript switches sessions", () => {
    const store = createAppStore();
    store.actions.installTranscriptBaseline("new-session", [
      { id: "a1", role: "assistant", content: "Current", status: "streaming", created_at: "t" } as Message,
    ], 0);
    applyPaintedLiveChunk(store, { sessionId: "old-session", messageId: "a1", kind: "parent" }, { token: " stale" });
    expect(store.state.messages[0]?.content).toBe("Current");
  });

  it("parseLiveStreamChunk reads reset and tool_calls", () => {
    const chunk = parseLiveStreamChunk(
      JSON.stringify({
        token: "hi",
        reset: true,
        tool_calls: [{ id: "t1", name: "command", args: {} }],
      }),
    );
    expect(chunk?.reset).toBe(true);
    expect(chunk?.tool_calls?.[0]?.name).toBe("command");
  });

  it("passes the caller signal to the stream request", async () => {
    const controller = new AbortController();
    let requestSignal: AbortSignal | undefined;
    const client = stubClient({
      streamSession: async (
        _sessionId: string,
        opts?: { signal?: AbortSignal },
      ) => {
        requestSignal = opts?.signal;
        return new Response(null, { status: 204 });
      },
    });

    await followSessionLiveStream({
      client,
      appStore: {} as AppStore,
      target: { sessionId: "session-1", messageId: "message-1", kind: "parent" },
      signal: controller.signal,
    });

    expect(requestSignal).toBe(controller.signal);
  });

  it("cancels a pending body read as soon as the signal aborts", async () => {
    let bodyCancelled = false;
    const body = new ReadableStream<Uint8Array>({
      cancel() {
        bodyCancelled = true;
      },
    });
    const client = stubClient({
      streamSession: async () => new Response(body),
    });
    const controller = new AbortController();

    const following = followSessionLiveStream({
      client,
      appStore: {} as AppStore,
      target: { sessionId: "session-1", messageId: "message-1", kind: "parent" },
      signal: controller.signal,
    });
    await Promise.resolve();
    controller.abort();
    await following;

    expect(bodyCancelled).toBe(true);
  });
});
