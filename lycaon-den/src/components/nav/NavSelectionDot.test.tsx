import { createSignal, Index, Show } from "solid-js";
import { render, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { NavSelectionDot, navRow } from "./NavSelectionDot.tsx";

/** Rows report their box the way the marker reads it: offsets in the list. */
function stubRow(el: Element, box: { top: number; height: number }): void {
  Object.defineProperty(el, "offsetTop", {
    value: box.top,
    configurable: true,
  });
  Object.defineProperty(el, "offsetHeight", {
    value: box.height,
    configurable: true,
  });
}

describe("NavSelectionDot", () => {
  it("centers on measured row geometry instead of a fixed pitch", async () => {
    const [index, setIndex] = createSignal(2);
    // Unequal row heights expose fixed-pitch placement.
    const heights = [30, 40, 50];
    let tops = 0;
    const rowTops = heights.map((h) => {
      const t = tops;
      tops += h + 1; // 1px gap
      return t;
    });

    const { container } = render(() => (
      <ul class="den-settings-sidebar-list" style={{ position: "relative" }}>
        <NavSelectionDot index={index()} />
        <Index each={heights}>
          {(_, i) => (
            <li {...navRow(i)}>
              <button type="button" class="den-shell-nav-sub-link">
                row {i}
              </button>
            </li>
          )}
        </Index>
      </ul>
    ));

    const dot = container.querySelector(".den-nav-marker") as HTMLElement;
    const rows = [...container.querySelectorAll("[data-nav-row]")];

    Object.defineProperty(dot, "offsetHeight", { value: 11, configurable: true });
    rows.forEach((row, i) => {
      stubRow(row, { top: rowTops[i]!, height: heights[i]! });
    });

    setIndex(2);
    // Toggling the index measures the stubbed geometry.
    setIndex(-1);
    setIndex(2);

    const expectedY = rowTops[2]! + (heights[2]! - 11) / 2;
    await waitFor(() => expect(dot.style.transform).toBe(`translateY(${expectedY}px)`));
    expect(expectedY).not.toBe(61.5);
  });

  // List-relative offsets remain stable during scrolling.
  it("places from offsets that a scrolled list does not shift", async () => {
    const [index, setIndex] = createSignal(1);
    const { container } = render(() => (
      <ul class="den-settings-sidebar-list" style={{ position: "relative" }}>
        <NavSelectionDot index={index()} />
        <li {...navRow(0)}>
          <button type="button" class="den-shell-nav-sub-link">
            a
          </button>
        </li>
        <li {...navRow(1)}>
          <button type="button" class="den-shell-nav-sub-link">
            b
          </button>
        </li>
      </ul>
    ));

    const list = container.querySelector("ul")!;
    const dot = container.querySelector(".den-nav-marker") as HTMLElement;
    const rows = [...container.querySelectorAll("[data-nav-row]")];
    Object.defineProperty(dot, "offsetHeight", { value: 11, configurable: true });
    rows.forEach((row, i) => stubRow(row, { top: i * 30, height: 30 }));

    setIndex(-1);
    setIndex(1);
    await waitFor(() => expect(dot.style.transform).toBe("translateY(39.5px)"));
    const placed = dot.style.transform;
    expect(placed).toBe("translateY(39.5px)");

    list.scrollTop = 120;
    setIndex(-1);
    setIndex(1);
    expect(dot.style.transform).toBe(placed);
  });

  it("hides when index is negative", async () => {
    const { container } = render(() => (
      <ul>
        <NavSelectionDot index={-1} />
        <li {...navRow(0)}>
          <button type="button" class="den-shell-nav-sub-link">
            a
          </button>
        </li>
      </ul>
    ));
    const dot = container.querySelector(".den-nav-marker") as HTMLElement;
    await waitFor(() => expect(dot.style.opacity).toBe("0"));
  });

  it("animates between indices and is not canceled by ResizeObserver", async () => {
    const [index, setIndex] = createSignal(0);
    const animate = vi.fn(
      (
        _keyframes: Keyframe[] | PropertyIndexedKeyframes | null,
        _options?: number | KeyframeAnimationOptions,
      ): Animation => {
        const animation = {
          onfinish: null as (() => void) | null,
          cancel: vi.fn(),
        };
        return animation as unknown as Animation;
      },
    );

    const { container } = render(() => (
      <ul class="den-settings-sidebar-list" style={{ position: "relative" }}>
        <NavSelectionDot index={index()} />
        <li {...navRow(0)}>
          <button type="button" class="den-shell-nav-sub-link">
            a
          </button>
        </li>
        <li {...navRow(1)}>
          <button type="button" class="den-shell-nav-sub-link">
            b
          </button>
        </li>
      </ul>
    ));

    const dot = container.querySelector(".den-nav-marker") as HTMLElement;
    const rows = [...container.querySelectorAll("[data-nav-row]")];
    Object.defineProperty(dot, "offsetHeight", { value: 11, configurable: true });
    Object.defineProperty(dot, "animate", { value: animate, configurable: true });
    stubRow(rows[0]!, { top: 0, height: 30 });
    stubRow(rows[1]!, { top: 31, height: 30 });

    setIndex(-1);
    setIndex(0);
    await waitFor(() => expect(dot.style.transform).toBe("translateY(9.5px)"));
    expect(animate).not.toHaveBeenCalled(); // first visible place snaps

    setIndex(1);
    await waitFor(() => expect(animate).toHaveBeenCalledTimes(1));
    expect(animate.mock.calls[0]![0]).toEqual([
      { transform: "translateY(9.5px)" },
      { transform: "translateY(40.5px)" },
    ]);
  });

  // Replacing a row control preserves subsequent row indices.
  it("keeps the index when an earlier row swaps its control out", async () => {
    const [renaming, setRenaming] = createSignal(false);
    const [index, setIndex] = createSignal(2);
    const { container } = render(() => (
      <ul style={{ position: "relative" }}>
        <NavSelectionDot index={index()} />
        <li {...navRow(0)}>
          <Show when={!renaming()} fallback={<input aria-label="Rename" />}>
            <button type="button" class="den-shell-nav-sub-link">
              a
            </button>
          </Show>
        </li>
        <li {...navRow(1)}>
          <button type="button" class="den-shell-nav-sub-link">
            b
          </button>
        </li>
        <li {...navRow(2)}>
          <button type="button" class="den-shell-nav-sub-link">
            c
          </button>
        </li>
      </ul>
    ));

    const dot = container.querySelector(".den-nav-marker") as HTMLElement;
    const rows = [...container.querySelectorAll("[data-nav-row]")];
    Object.defineProperty(dot, "offsetHeight", { value: 11, configurable: true });
    rows.forEach((row, i) => stubRow(row, { top: i * 30, height: 30 }));

    setRenaming(true);
    // Toggling the index measures the stubbed geometry.
    setIndex(-1);
    setIndex(2);

    await waitFor(() => expect(dot.style.opacity).toBe("1"));
    expect(dot.style.transform).toBe("translateY(69.5px)");
  });
});
