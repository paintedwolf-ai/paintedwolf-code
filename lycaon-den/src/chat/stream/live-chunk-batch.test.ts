import { describe, expect, it, vi } from "vitest";
import { createLiveChunkBatch } from "./live-chunk-batch.ts";
import { applyLiveStreamChunkToMessage } from "./session-live-stream.ts";
import type { Message } from "../../api/types.ts";

function fixture() {
  let frame: FrameRequestCallback | undefined;
  const apply = vi.fn();
  const batch = createLiveChunkBatch(apply, {
    request: (callback) => { frame = callback; return 1; },
    cancel: () => { frame = undefined; },
  });
  return { apply, batch, paint: () => frame?.(0) };
}

describe("live chunk batching", () => {
  it("renders first content immediately and combines later tokens at paint", () => {
    const f = fixture();
    f.batch.push({ token: "First" });
    expect(f.apply).toHaveBeenCalledExactlyOnceWith({ token: "First" });
    f.batch.push({ token: " second" });
    f.batch.push({ token: " third" });
    expect(f.apply).toHaveBeenCalledOnce();
    f.paint();
    expect(f.apply).toHaveBeenLastCalledWith({ token: " second third", reset: undefined });
    expect(f.apply).toHaveBeenCalledTimes(2);
  });

  it("preserves reset, tool-call, and completion semantics across a batch", () => {
    const f = fixture();
    f.batch.push({ token: "old" });
    const chunks = [
      { token: " discarded" },
      { token: "replacement", reset: true },
      { token: " text", reset: false },
      { tool_calls: [{ id: "t1", name: "read", args: {} }] },
      { done: true },
    ];
    chunks.forEach(f.batch.push);
    let sequential = { content: "old", status: "streaming" } as Message;
    for (const chunk of chunks) sequential = applyLiveStreamChunkToMessage(sequential, chunk);
    const combined = applyLiveStreamChunkToMessage({ content: "old", status: "streaming" } as Message, f.apply.mock.calls[1]![0]);
    expect(combined).toEqual(sequential);
    f.paint();
    expect(f.apply).toHaveBeenCalledTimes(2);
  });

  it("flushes EOF text and drops aborted queued text", () => {
    const f = fixture();
    f.batch.push({ token: "a" });
    f.batch.push({ token: "b" });
    f.batch.flush();
    expect(f.apply).toHaveBeenCalledTimes(2);
    f.batch.push({ token: "stale" });
    f.batch.cancel();
    f.paint();
    expect(f.apply).toHaveBeenCalledTimes(2);
  });
});
