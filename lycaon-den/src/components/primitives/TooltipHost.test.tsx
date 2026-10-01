import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipHost } from "./TooltipHost.tsx";

describe("TooltipHost", () => {
  beforeEach(() => vi.useFakeTimers());

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("portals delegated data-tip labels onto the shared anchored layer", async () => {
    render(() => (
      <>
        <button
          type="button"
          data-tip="Restore these lines"
          data-tip-pos="end"
        >
          Reject
        </button>
        <TooltipHost />
      </>
    ));

    const button = screen.getByRole("button", { name: "Reject" });
    fireEvent.mouseOver(button);
    await vi.advanceTimersByTimeAsync(349);
    expect(screen.queryByRole("tooltip")).toBeNull();
    await vi.advanceTimersByTimeAsync(1);

    const tooltip = screen.getByRole("tooltip");
    expect(tooltip.textContent).toBe("Restore these lines");
    expect(document.body.contains(tooltip)).toBe(true);
    expect(tooltip.dataset.denAnchoredSurface).toBeTruthy();
    expect(tooltip.style.zIndex).toBe("var(--den-z-anchored-surface)");
    expect(tooltip.dataset.side).toBe("right");

    fireEvent.mouseOut(button);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("uses the same layer for keyboard focus and honors below placement", async () => {
    render(() => (
      <>
        <button
          type="button"
          data-tip="Open files"
          data-tip-pos="below"
        >
          Files
        </button>
        <TooltipHost />
      </>
    ));

    const button = screen.getByRole("button", { name: "Files" });
    fireEvent.focusIn(button);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").dataset.side).toBe("bottom");

    fireEvent.focusOut(button);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("shows conditional tips only when their visible label is hidden or clipped", async () => {
    render(() => (
      <>
        <button
          type="button"
          data-tip="Progress"
          data-tip-when-clipped=".label"
        >
          <span class="label">Progress</span>
        </button>
        <TooltipHost />
      </>
    ));

    const button = screen.getByRole("button", { name: "Progress" });
    const label = button.querySelector<HTMLElement>(".label")!;
    fireEvent.mouseOver(button);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.queryByRole("tooltip")).toBeNull();

    label.style.display = "none";
    fireEvent.mouseOut(button);
    fireEvent.mouseOver(button);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe("Progress");
  });

  it("supports clipping checks on the tooltip anchor itself", async () => {
    render(() => (
      <>
        <span data-tip="A long path" data-tip-when-clipped>
          A long path
        </span>
        <TooltipHost />
      </>
    ));

    const label = screen.getByText("A long path");
    Object.defineProperties(label, {
      clientWidth: { configurable: true, value: 40 },
      scrollWidth: { configurable: true, value: 80 },
    });
    fireEvent.mouseOver(label);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe("A long path");
  });

  it("dismisses a visible tip when its anchor disappears", async () => {
    render(() => (
      <>
        <button type="button" data-tip="Back to the current file">
          Current
        </button>
        <button type="button" data-tip="Copy path">
          Copy
        </button>
        <TooltipHost />
      </>
    ));

    const button = screen.getByRole("button", { name: "Current" });
    fireEvent.mouseOver(button);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe(
      "Back to the current file",
    );

    fireEvent.focusIn(button);
    button.remove();
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByRole("tooltip")).toBeNull();

    fireEvent.mouseOver(screen.getByRole("button", { name: "Copy" }));
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe("Copy path");
  });

  it("drops the tip of a control whose click hides the pane it lives in", async () => {
    const [collapsed, setCollapsed] = createSignal(false);
    render(() => (
      <>
        <aside aria-hidden={collapsed()} inert={collapsed() ? true : undefined}>
          <button
            type="button"
            data-tip="Hide sidebar"
            onClick={() => setCollapsed(true)}
          >
            Hide
          </button>
        </aside>
        <TooltipHost />
      </>
    ));

    const button = screen.getByRole("button", { name: "Hide" });
    fireEvent.mouseOver(button);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe("Hide sidebar");

    fireEvent.click(button);
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("shows the tip of a disabled control, which dispatches no pointer events", async () => {
    render(() => (
      <>
        <div>
          <button type="button" disabled data-tip="Needs a repository">
            Land
          </button>
        </div>
        <TooltipHost />
      </>
    ));

    const button = screen.getByRole("button", { name: "Land" });
    button.getBoundingClientRect = () =>
      ({ left: 10, right: 90, top: 10, bottom: 30 }) as DOMRect;
    // The event targets the parent: a disabled control never receives one.
    fireEvent.mouseMove(button.parentElement as HTMLElement, {
      clientX: 50,
      clientY: 20,
    });
    await vi.advanceTimersByTimeAsync(350);
    expect(screen.getByRole("tooltip").textContent).toBe("Needs a repository");

    fireEvent.mouseMove(button.parentElement as HTMLElement, {
      clientX: 500,
      clientY: 500,
    });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
