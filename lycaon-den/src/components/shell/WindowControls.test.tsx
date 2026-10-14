import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";

let platform: "macos" | "linux" = "linux";
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  tauriPlatform: () => platform,
  isTauriRuntime: () => true,
}));

const minimize = vi.fn(async () => undefined);
const toggleMaximize = vi.fn(async () => undefined);
const close = vi.fn(async () => undefined);
vi.mock("../../platform/windows/current-window.ts", () => ({
  currentWindow: async () => ({ minimize, toggleMaximize, close }),
}));

vi.mock("./AppMenuButton.tsx", () => ({
  AppMenuButton: (props: { side: string }) => <button type="button" tabindex="-1" data-testid="window-app-menu" data-side={props.side} />,
}));

const { WindowControls, soleWindowControls } = await import("./WindowControls.tsx");
const { setWindowChromeLayoutForTests } = await import("../../platform/windows/window-chrome-layout.ts");
const { setWindowMaximizedForTests } = await import("../../platform/windows/window-maximized.ts");

beforeEach(() => {
  platform = "linux";
  minimize.mockClear();
  toggleMaximize.mockClear();
  close.mockClear();
  setWindowMaximizedForTests(false);
});

afterEach(() => {
  cleanup();
  setWindowChromeLayoutForTests(null);
});

describe("WindowControls", () => {
  it("draws the default cluster on the right with accessible names", () => {
    render(() => <WindowControls side="right" owner narrow />);
    const cluster = screen.getByTestId("window-controls-right");
    expect(cluster.getAttribute("data-side")).toBe("right");
    expect(
      [...cluster.querySelectorAll("button")].map((button) => button.getAttribute("aria-label") ?? button.dataset.testid),
    ).toEqual(["window-app-menu", "Minimize window", "Maximize window", "Close window"]);
    expect(screen.queryByTestId("window-controls-left")).toBeNull();
    // Native decorations are never Tab stops.
    for (const button of cluster.querySelectorAll("button")) expect(button.getAttribute("tabindex")).toBe("-1");
  });

  it("follows the desktop's layout for each side", () => {
    setWindowChromeLayoutForTests({ left: ["appmenu"], right: ["close"], source: "portal" });
    render(() => (
      <>
        <WindowControls side="left" owner narrow />
        <WindowControls side="right" owner narrow />
      </>
    ));
    expect(screen.getByTestId("window-controls-left").querySelector("[data-testid='window-app-menu']")).not.toBeNull();
    expect(screen.queryByTestId("window-minimize")).toBeNull();
    expect(screen.getByTestId("window-controls-right").querySelector("[data-testid='window-close']")).not.toBeNull();
  });

  it("drives the window through its native operations", () => {
    render(() => <WindowControls side="right" owner narrow />);
    fireEvent.click(screen.getByTestId("window-minimize"));
    fireEvent.click(screen.getByTestId("window-maximize"));
    fireEvent.click(screen.getByTestId("window-close"));
    return vi.waitFor(() => {
      expect(minimize).toHaveBeenCalledTimes(1);
      expect(toggleMaximize).toHaveBeenCalledTimes(1);
      expect(close).toHaveBeenCalledTimes(1);
    });
  });

  it("labels the maximize control Restore while the window is maximized", () => {
    render(() => <WindowControls side="right" owner narrow />);
    const control = screen.getByTestId("window-maximize");
    expect(control.getAttribute("aria-label")).toBe("Maximize window");
    setWindowMaximizedForTests(true);
    expect(control.getAttribute("aria-label")).toBe("Restore window");
    expect(control.hasAttribute("data-maximized")).toBe(true);
  });

  it("marks the cluster the narrow band swaps in or out", () => {
    render(() => (
      <>
        <WindowControls side="right" owner narrow={false} />
        <WindowControls side="left" owner={false} narrow />
      </>
    ));
    setWindowChromeLayoutForTests({ left: ["close"], right: ["minimize"], source: "portal" });
    expect(screen.getByTestId("window-controls-right").hasAttribute("data-narrow-hidden")).toBe(true);
    expect(screen.getByTestId("window-controls-left").hasAttribute("data-narrow-only")).toBe(true);
  });

  it("renders nothing for a column that never owns the edge", () => {
    render(() => <WindowControls side="right" owner={false} narrow={false} />);
    expect(screen.queryByTestId("window-controls-right")).toBeNull();
  });

  it("renders nothing where the window has native buttons", () => {
    platform = "macos";
    const { container } = render(() => (
      <>
        <WindowControls side="right" owner narrow />
        {soleWindowControls()}
      </>
    ));
    expect(container.querySelector("button")).toBeNull();
  });

  it("gives a lone surface both edges", () => {
    setWindowChromeLayoutForTests({ left: ["appmenu"], right: ["close"], source: "portal" });
    render(() => <>{soleWindowControls()}</>);
    expect(screen.getByTestId("window-controls-left")).not.toBeNull();
    expect(screen.getByTestId("window-controls-right")).not.toBeNull();
  });
});
