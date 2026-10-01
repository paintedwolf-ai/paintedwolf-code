// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ScrollbarUpdates } from "./scrollbar-updates.ts";

describe("scrollbar updates", () => {
  const frames = new Map<number, FrameRequestCallback>();
  let sequence = 0;
  let queue: ScrollbarUpdates;
  const blocked = new Set<HTMLElement>();
  const update = vi.fn<(host: HTMLElement) => void>();

  const host = () => document.createElement("div");
  const paint = () => {
    const batch = [...frames];
    frames.clear();
    for (const [, callback] of batch) callback(0);
  };

  beforeEach(() => {
    frames.clear();
    blocked.clear();
    update.mockReset();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++sequence, callback);
      return sequence;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
    queue = new ScrollbarUpdates((element) => !blocked.has(element), update);
  });

  afterEach(() => {
    queue.clear();
    vi.unstubAllGlobals();
  });

  it("coalesces hosts into one frame and keeps blocked work dormant", () => {
    const ready = host();
    const held = host();
    blocked.add(held);
    queue.request(held);
    queue.resumeConnected();
    expect(frames.size).toBe(0);

    queue.request(ready);
    queue.request(ready);
    queue.resumeConnected();
    expect(frames.size).toBe(1);
    paint();
    expect(update.mock.calls).toEqual([[ready]]);
    expect(frames.size).toBe(0);

    blocked.delete(held);
    queue.resume(held);
    paint();
    expect(update.mock.calls).toEqual([[ready], [held]]);
    queue.resumeConnected();
    expect(frames.size).toBe(0);
  });

  it("rechecks a hold that begins after scheduling", () => {
    const element = host();
    queue.request(element);
    blocked.add(element);
    paint();
    expect(update).not.toHaveBeenCalled();
    expect(frames.size).toBe(0);
    blocked.delete(element);
    queue.resumeConnected();
    paint();
    expect(update.mock.calls).toEqual([[element]]);
  });

  it("does not create work on a reconnect or input settlement", () => {
    queue.resume(host());
    queue.resumeConnected();
    expect(frames.size).toBe(0);
    expect(update).not.toHaveBeenCalled();
  });

  it("defers requests made by an update until the next frame", () => {
    const element = host();
    const added = host();
    update.mockImplementationOnce(() => {
      queue.request(element);
      queue.request(added);
      queue.resumeConnected();
    });
    queue.request(element);
    paint();
    expect(update.mock.calls).toEqual([[element]]);
    expect(frames.size).toBe(1);
    paint();
    expect(update.mock.calls).toEqual([[element], [element], [added]]);
    expect(frames.size).toBe(0);
  });

  it("does not deliver an old request after another update replaces its host", () => {
    const first = host();
    const replaced = host();
    update.mockImplementationOnce(() => {
      queue.forget(replaced);
      queue.request(replaced);
    });
    queue.request(first);
    queue.request(replaced);
    paint();
    expect(update.mock.calls).toEqual([[first]]);
    paint();
    expect(update.mock.calls).toEqual([[first], [replaced]]);
  });

  it("defers newer geometry requested for a host already in the current batch", () => {
    const first = host();
    const second = host();
    update.mockImplementationOnce(() => queue.request(second));
    queue.request(first);
    queue.request(second);
    paint();
    expect(update.mock.calls).toEqual([[first]]);
    expect(frames.size).toBe(1);
    paint();
    expect(update.mock.calls).toEqual([[first], [second]]);
  });

  it("checks each host again when an earlier update changes its readiness", () => {
    const first = host();
    const second = host();
    update.mockImplementationOnce(() => blocked.add(second));
    queue.request(first);
    queue.request(second);
    paint();
    expect(update.mock.calls).toEqual([[first]]);
    expect(frames.size).toBe(0);
    blocked.delete(second);
    queue.resume(second);
    paint();
    expect(update.mock.calls).toEqual([[first], [second]]);
  });

  it("cancels released work and ignores a stale frame after reset", () => {
    const oldHost = host();
    queue.request(oldHost);
    const staleFrame = [...frames.values()][0]!;
    queue.clear();
    expect(frames.size).toBe(0);
    const newHost = host();
    queue.request(newHost);
    staleFrame(0);
    expect(update).not.toHaveBeenCalled();
    expect(frames.size).toBe(1);
    queue.forget(newHost);
    expect(frames.size).toBe(0);
    queue.resumeConnected();
    expect(frames.size).toBe(0);
  });

  it("drops the rest of a batch when an update clears the queue", () => {
    update.mockImplementationOnce(() => queue.clear());
    queue.request(host());
    queue.request(host());
    paint();
    expect(update).toHaveBeenCalledOnce();
    expect(frames.size).toBe(0);
  });
});
