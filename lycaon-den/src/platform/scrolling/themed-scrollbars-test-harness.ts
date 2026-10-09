import { expect, vi } from "vitest";
import { OverlayScrollbars } from "overlayscrollbars";
import { scrollportFrameParts, type ScrollportAxis } from "./scrollport-frame-dom.ts";
import { DEN_SCROLLPORT_AXIS_ATTR } from "./themed-scrollbars.ts";

export type MockInstance = {
  destroy: ReturnType<typeof vi.fn>;
  sleep: ReturnType<typeof vi.fn>;
  update: ReturnType<typeof vi.fn>;
  on: ReturnType<typeof vi.fn>;
  state: ReturnType<typeof vi.fn>;
  elements: () => Record<string, never>;
};

vi.mock("./overlay-scrollbar-input.ts", () => ({
  bindOverlayScrollbarInput: vi.fn(() => ({ dispose: vi.fn(), refreshGeometry: vi.fn() })),
}));

vi.mock("./overlay-scrollbar-autohide.ts", () => ({
  bindOverlayScrollbarAutoHide: vi.fn(() => vi.fn()),
}));

vi.mock("overlayscrollbars", () => {
  const instances = new WeakMap<Element, MockInstance>();
  const factory = vi.fn(
    (target: Element | { target: Element }, options?: unknown) => {
      const host = target instanceof Element ? target : target.target;
      const prior = instances.get(host);
      if (!options) return prior;
      if (prior) return prior;
      const instance: MockInstance = {
        destroy: vi.fn(() => instances.delete(host)),
        sleep: vi.fn(),
        update: vi.fn(),
        on: vi.fn(() => () => {}),
        state: vi.fn(() => ({ overflowStyle: { x: "scroll", y: "scroll" } })),
        elements: () => ({}),
      };
      instances.set(host, instance);
      return instance;
    },
  ) as unknown as ReturnType<typeof vi.fn> & { nonce: ReturnType<typeof vi.fn> };
  factory.nonce = vi.fn();
  return { OverlayScrollbars: factory };
});

export type ScrollbarConstruction = {
  init: { target: Element; elements?: { viewport?: Element } };
  options: Record<string, unknown>;
};

export function construction(host: Element): ScrollbarConstruction | undefined {
  const call = vi
    .mocked(OverlayScrollbars)
    .mock.calls.find(
      (entry) =>
        entry.length > 1 &&
        typeof entry[0] === "object" &&
        (entry[0] as { target?: Element }).target === host,
    );
  if (!call) return undefined;
  return {
    init: call[0] as ScrollbarConstruction["init"],
    options: call[1],
  };
}

export function instance(host: HTMLElement): MockInstance {
  return OverlayScrollbars(host) as unknown as MockInstance;
}

export function frameHtml(axis: ScrollportAxis = "y", classes = "", body = ""): string {
  return `<div class="den-scrollport ${classes}" ${DEN_SCROLLPORT_AXIS_ATTR}="${axis}">` +
    `<div class="den-scrollport__viewport"><div class="den-scrollport__content">${body}</div></div></div>`;
}

export type Frame = { frame: HTMLElement; viewport: HTMLElement; content: HTMLElement };

export function makeFrame(axis: ScrollportAxis = "y", classes = ""): Frame {
  const holder = document.createElement("div");
  holder.innerHTML = frameHtml(axis, classes);
  const frame = holder.firstElementChild as HTMLElement;
  const parts = scrollportFrameParts(frame);
  expect(parts, "valid scrollport markup").toBeDefined();
  return { frame, viewport: parts!.viewport, content: parts!.content };
}

/** Mocked geometry for the element the library scrolls. */
export function giveVerticalBar(host: HTMLElement, viewport: HTMLElement): HTMLElement {
  const bar = document.createElement("div");
  bar.className = "os-scrollbar os-scrollbar-vertical";
  host.append(bar);
  (instance(host) as unknown as { elements: () => unknown }).elements = () => ({
    scrollOffsetElement: viewport,
    scrollbarVertical: { scrollbar: bar },
  });
  return bar;
}
