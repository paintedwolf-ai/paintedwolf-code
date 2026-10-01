// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { bindScrollportMotion, unbindScrollportMotion } from "../../../platform/scrolling/scrollport-motion.ts";
import { bindReaderViewportInput } from "./source-reader-viewport-input.ts";

it("settles boundary input without a scroll and distinguishes scrollbar intent from restoration", () => {
  vi.useFakeTimers();
  const host = document.createElement("div"), viewport = document.createElement("div"), content = document.createElement("div");
  host.appendChild(viewport); viewport.appendChild(content);
  Object.defineProperties(viewport, {
    scrollHeight: { configurable: true, value: 1000 },
    clientHeight: { configurable: true, value: 100 },
    scrollTop: { configurable: true, writable: true, value: 50 },
  });
  const input = vi.fn(), settled = vi.fn();
  const motion = bindScrollportMotion(host, viewport, content);
  const stop = bindReaderViewportInput(host, input, settled);
  try {
    motion.noteNativeInput("wheel");
    expect(input).toHaveBeenLastCalledWith(true);
    vi.advanceTimersByTime(1000);
    expect(settled).toHaveBeenCalledOnce();
    input.mockClear();
    motion.commit(100, "thumb_drag", { measuredMaxOffset: 900 });
    expect(input).toHaveBeenCalledExactlyOnceWith(false);
    motion.commit(200, "restore_anchor", { measuredMaxOffset: 900 });
    expect(input).toHaveBeenCalledOnce();
    motion.beginThumbGesture();
    expect(input).toHaveBeenLastCalledWith(false);
    motion.endThumbGesture();
    expect(input).toHaveBeenLastCalledWith(false);
    host.dispatchEvent(new KeyboardEvent("keydown", { key: "Home" }));
    expect(input).toHaveBeenLastCalledWith(false);
  } finally {
    stop(); unbindScrollportMotion(host); vi.useRealTimers();
  }
});
