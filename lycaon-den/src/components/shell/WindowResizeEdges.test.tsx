import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";

let platform: "macos" | "linux" = "linux";
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  tauriPlatform: () => platform,
  isTauriRuntime: () => true,
}));

const startResizeDragging = vi.fn(async (_direction: string) => undefined);
vi.mock("../../platform/windows/current-window.ts", () => ({
  currentWindow: async () => ({ startResizeDragging }),
}));

const { WindowResizeEdges } = await import("./WindowResizeEdges.tsx");
const { setWindowFullscreenForTests, setWindowMaximizedForTests } = await import("../../platform/windows/window-maximized.ts");

beforeEach(() => {
  platform = "linux";
  startResizeDragging.mockClear();
  setWindowMaximizedForTests(false);
  setWindowFullscreenForTests(false);
});

afterEach(() => cleanup());

describe("WindowResizeEdges", () => {
  it("covers every edge and corner", () => {
    render(() => <WindowResizeEdges />);
    const zones = [...screen.getByTestId("window-resize-edges").querySelectorAll("[data-direction]")]
      .map((zone) => zone.getAttribute("data-direction"));
    expect(zones).toEqual(["North", "South", "East", "West", "NorthWest", "NorthEast", "SouthWest", "SouthEast"]);
  });

  it("hands a primary press to the window system's resize gesture", async () => {
    render(() => <WindowResizeEdges />);
    const east = screen.getByTestId("window-resize-edges").querySelector("[data-direction='East']")!;
    fireEvent.pointerDown(east, { button: 2 });
    fireEvent.pointerDown(east, { button: 0 });
    await vi.waitFor(() => expect(startResizeDragging).toHaveBeenCalledTimes(1));
    expect(startResizeDragging).toHaveBeenCalledWith("East");
  });

  it("survives a compositor that refuses the resize gesture", async () => {
    const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
    startResizeDragging.mockRejectedValueOnce(new Error("no seat"));
    render(() => <WindowResizeEdges />);
    fireEvent.pointerDown(screen.getByTestId("window-resize-edges").querySelector("[data-direction='South']")!, { button: 0 });
    await vi.waitFor(() => expect(debug).toHaveBeenCalledWith("[window-chrome] resize drag refused", expect.any(Error)));
    debug.mockRestore();
  });

  it("stands down while the window is maximized or fullscreen", () => {
    render(() => <WindowResizeEdges />);
    setWindowMaximizedForTests(true);
    expect(screen.queryByTestId("window-resize-edges")).toBeNull();
    setWindowMaximizedForTests(false);
    expect(screen.queryByTestId("window-resize-edges")).not.toBeNull();
    setWindowFullscreenForTests(true);
    expect(screen.queryByTestId("window-resize-edges")).toBeNull();
  });

  it("renders nothing where the compositor draws the border", () => {
    platform = "macos";
    const { container } = render(() => <WindowResizeEdges />);
    expect(container.firstChild).toBeNull();
  });
});
