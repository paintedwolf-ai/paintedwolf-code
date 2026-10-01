// @vitest-environment jsdom
import { render } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { For } from "solid-js";
import { createFittedColumns } from "./create-fitted-columns.ts";
import { resetSharedResizeObserverForTests } from "../layout/shared-resize-observer.ts";
import { resolveColumnWidths, type ColumnSpec } from "./list-columns.ts";

/** Narrowing sheds a column; widening restores it. */

type Observed = { target: Element; cb: ResizeObserverCallback };
const observed: Observed[] = [];

class FakeResizeObserver {
  constructor(private readonly cb: ResizeObserverCallback) {}
  observe(target: Element) {
    observed.push({ target, cb: this.cb });
  }
  unobserve(target: Element) {
    for (let i = observed.length - 1; i >= 0; i--) {
      if (observed[i]?.target === target && observed[i]?.cb === this.cb) observed.splice(i, 1);
    }
  }
  disconnect() {
    for (let i = observed.length - 1; i >= 0; i--) {
      if (observed[i]?.cb === this.cb) observed.splice(i, 1);
    }
  }
}

/** Delivers a resize and awaits the observer's microtask batch. */
async function emit(target: Element, width: number): Promise<void> {
  const entry = { target, contentRect: { width, height: 32 } } as ResizeObserverEntry;
  for (const o of [...observed]) {
    if (o.target === target) o.cb([entry], {} as ResizeObserver);
  }
  await Promise.resolve();
}

const COLUMNS: readonly ColumnSpec<never>[] = [
  { key: "a", label: "A", width: { basis: 100, min: 100, max: 100 } },
  { key: "grow", label: "Grow", width: { basis: 200, min: 120, max: 600, grow: true } },
  { key: "b", label: "B", width: { basis: 100, min: 100, max: 100 }, optional: true },
];

// Every column at rest: 100 + the growing column's 120 floor + 100, two gaps.
const FULL = 344;
// Without the optional column: 100 + 120 and one gap.
const SHED = 232;

function mount() {
  let el!: HTMLDivElement;
  const result = render(() => {
    const fitted = createFittedColumns<never>({
      columns: () => COLUMNS,
      widths: () => resolveColumnWidths(COLUMNS),
    });
    return (
      <div
        ref={(node) => {
          el = node;
          fitted.measure(node);
        }}
        data-testid="probe"
        style={{ "--den-list-cols": fitted.template() }}
      >
        <For each={fitted.columns()}>
          {(col) => <span data-column={col.key} />}
        </For>
      </div>
    );
  });
  return { ...result, el: () => el };
}

function drawn(container: HTMLElement): string[] {
  return [...container.querySelectorAll("[data-column]")].map(
    (node) => (node as HTMLElement).dataset.column ?? "",
  );
}

describe("fitted columns", () => {
  beforeEach(() => {
    observed.length = 0;
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    resetSharedResizeObserverForTests();
  });
  afterEach(() => {
    resetSharedResizeObserverForTests();
    vi.unstubAllGlobals();
  });

  it("draws every column until it has been measured", () => {
    const { container } = mount();
    expect(drawn(container)).toEqual(["a", "grow", "b"]);
  });

  it("sheds when the tracks stop fitting and restores when they fit again", async () => {
    const { container, el } = mount();

    await emit(el(), SHED);
    expect(drawn(container)).toEqual(["a", "grow"]);

    await emit(el(), FULL);
    expect(drawn(container)).toEqual(["a", "grow", "b"]);

    await emit(el(), FULL - 1);
    expect(drawn(container)).toEqual(["a", "grow"]);
  });

  // A shed column loses its track as well as its cell.
  it("keeps the template and the drawn cells in step", async () => {
    const { container, el } = mount();
    const tracks = () =>
      (container.querySelector("[data-testid='probe']") as HTMLElement)
        .style.getPropertyValue("--den-list-cols")
        .match(/minmax\([^)]*\)/gu) ?? [];

    await emit(el(), SHED);
    expect(tracks()).toHaveLength(drawn(container).length);
    expect(drawn(container)).toEqual(["a", "grow"]);

    await emit(el(), FULL);
    expect(tracks()).toHaveLength(drawn(container).length);
    expect(drawn(container)).toEqual(["a", "grow", "b"]);
  });

  it("stops observing when the list unmounts", async () => {
    const { unmount, el } = mount();
    await emit(el(), SHED);
    expect(observed.length).toBe(1);
    unmount();
    expect(observed.length).toBe(0);
  });
});
