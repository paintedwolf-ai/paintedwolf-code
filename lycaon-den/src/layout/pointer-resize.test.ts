// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { startPointerResize } from "./pointer-resize.ts";

afterEach(() => { vi.useRealTimers(); });
function gesture() {
  vi.useFakeTimers();
  const session = { preview: vi.fn(), commit: vi.fn(), cancel: vi.fn() };
  const stop = startPointerResize({
    handle: document.createElement("div"), pointerId: 1, startPosition: 100,
    positionOf: (e) => e.clientX, valueAt: (x) => x, session,
  });
  const send = (type: string, x: number) => {
    const event = new Event(type);
    Object.assign(event, { pointerId: 1, clientX: x });
    window.dispatchEvent(event);
  };
  return { session, send, stop };
}
describe("live pointer resize", () => {
  it("publishes the latest position each frame and the exact final position on release", () => {
    const g = gesture();
    g.send("pointermove", 110);
    g.send("pointermove", 140);
    expect(g.session.preview).not.toHaveBeenCalled();
    vi.advanceTimersToNextFrame();
    expect(g.session.preview.mock.calls).toEqual([[140]]);
    g.send("pointermove", 170);
    g.send("pointerup", 180);
    vi.runAllTimers();
    expect(g.session.preview.mock.calls).toEqual([[140], [180]]);
    expect(g.session.commit).toHaveBeenCalledOnce();
    g.stop();
    expect(g.session.cancel).not.toHaveBeenCalled();
  });
  it("cancels pending previews on interruption", () => {
    const g = gesture();
    g.send("pointermove", 140);
    window.dispatchEvent(new Event("blur"));
    vi.runAllTimers();
    expect(g.session.preview).not.toHaveBeenCalled();
    expect(g.session.cancel).toHaveBeenCalledOnce();
    g.stop();
  });
});
