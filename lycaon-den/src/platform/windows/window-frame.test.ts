// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

let platform: "macos" | "linux" = "linux";
let tauri = true;
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  tauriPlatform: () => platform,
  isTauriRuntime: () => tauri,
}));

const invoke = vi.fn(async (_command: string): Promise<unknown> => undefined);
vi.mock("@tauri-apps/api/core", () => ({
  invoke: (command: string) => invoke(command),
}));

type FrameHandler = (event: { payload: unknown }) => void;
let frameChanged: FrameHandler | null = null;
const listen = vi.fn(async (_event: string, handler: FrameHandler) => {
  frameChanged = handler;
  return () => undefined;
});
vi.mock("./current-window.ts", () => ({
  currentWindow: async () => ({ listen }),
}));

const { readWindowFrame, trackWindowFrame, WINDOW_FRAME_ATTR, WINDOW_FRAME_CHANGED_EVENT } = await import(
  "./window-frame.ts"
);

const floatingFrame = { floating: true, content_width: 900, content_height: 600 };
const frameAttr = () => document.documentElement.getAttribute(WINDOW_FRAME_ATTR);

beforeEach(() => {
  platform = "linux";
  tauri = true;
  frameChanged = null;
  invoke.mockReset();
  listen.mockClear();
  document.documentElement.removeAttribute(WINDOW_FRAME_ATTR);
});

describe("trackWindowFrame", () => {
  it("rounds the page once the host says the window floats", async () => {
    invoke.mockResolvedValueOnce(floatingFrame);
    await trackWindowFrame();
    expect(invoke).toHaveBeenCalledWith("linux_window_frame");
    expect(frameAttr()).toBe("floating");
  });

  it("squares the page while the window is maximized, tiled, or fullscreen", async () => {
    invoke.mockResolvedValueOnce(floatingFrame);
    await trackWindowFrame();
    expect(listen).toHaveBeenCalledWith(WINDOW_FRAME_CHANGED_EVENT, expect.any(Function));
    frameChanged?.({ payload: { ...floatingFrame, floating: false } });
    expect(frameAttr()).toBe("fitted");
    frameChanged?.({ payload: "not a frame" });
    expect(frameAttr()).toBe("fitted");
  });

  it("keeps a change that lands during the first read", async () => {
    invoke.mockImplementationOnce(async () => {
      frameChanged?.({ payload: { ...floatingFrame, floating: false } });
      return floatingFrame;
    });
    await trackWindowFrame();
    expect(frameAttr()).toBe("fitted");
  });

  it("stays square when the host cannot answer", async () => {
    invoke.mockRejectedValueOnce(new Error("no window"));
    await trackWindowFrame();
    expect(frameAttr()).toBe("fitted");
  });

  it("leaves other platforms and the browser alone", async () => {
    platform = "macos";
    await trackWindowFrame();
    tauri = false;
    platform = "linux";
    await trackWindowFrame();
    expect(invoke).not.toHaveBeenCalled();
    expect(frameAttr()).toBeNull();
  });
});

describe("readWindowFrame", () => {
  it("returns the content size without the frame's shadow", async () => {
    invoke.mockResolvedValueOnce(floatingFrame);
    expect(await readWindowFrame()).toEqual(floatingFrame);
  });

  it("rejects a malformed answer", async () => {
    invoke.mockResolvedValueOnce({ floating: "yes" });
    expect(await readWindowFrame()).toBeNull();
  });
});
