// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const flush = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock("./files-hot-exit.ts", () => ({ flushFilesHotExitToDisk: flush }));

import { watchWindowExitForHotExit } from "./files-hot-exit-window.ts";

describe("hot exit window flush", () => {
  afterEach(() => flush.mockClear());

  it("flushes when the window hides and on the way out", async () => {
    const dispose = watchWindowExitForHotExit();

    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(flush).toHaveBeenCalledTimes(1);
    await new Promise((resolve) => setTimeout(resolve, 0));

    window.dispatchEvent(new Event("pagehide"));
    expect(flush).toHaveBeenCalledTimes(2);
    await new Promise((resolve) => setTimeout(resolve, 0));

    dispose();
    window.dispatchEvent(new Event("pagehide"));
    expect(flush).toHaveBeenCalledTimes(2);
  });

  it("ignores a return to the foreground", () => {
    const dispose = watchWindowExitForHotExit();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(flush).not.toHaveBeenCalled();
    dispose();
  });
});
