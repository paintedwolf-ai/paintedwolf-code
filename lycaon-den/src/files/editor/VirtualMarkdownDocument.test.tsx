import { afterEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { createVirtualizer, type VirtualItem } from "@tanstack/solid-virtual";
import type { Token } from "marked";
import { VirtualMarkdownDocument } from "./VirtualMarkdownDocument.tsx";
import { bindScrollportMotion, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";

vi.mock("@tanstack/solid-virtual", () => ({ createVirtualizer: vi.fn() }));
afterEach(() => vi.clearAllMocks());

const row = (index: number): VirtualItem => ({ index, key: index, start: index * 100, size: 100, end: (index + 1) * 100, lane: 0 });
const tokens = (index: number): Token[] => [{
  type: "paragraph", raw: `Section ${index}`, text: `Section ${index}`,
  tokens: [{ type: "text", raw: `Section ${index}`, text: `Section ${index}` }],
}];

describe("Markdown preview scroll window", () => {
  it("retains painted text until the destination is ready and reuses recent sections", async () => {
    const [items, setItems] = createSignal([row(0), row(1)]);
    const measureElement = vi.fn((element: HTMLElement) => {
      expect(element.textContent).toContain("Section");
    });
    const instance = {
      getVirtualItems: items,
      getTotalSize: () => 2000,
      itemSizeCache: new Map(),
      measureElement,
    } as unknown as ReturnType<typeof createVirtualizer>;
    vi.mocked(createVirtualizer).mockImplementation((options) => {
      options.rangeExtractor?.({ startIndex: 0, endIndex: 1, overscan: 2, count: 20 });
      return instance;
    });
    const receivers = new Map<number, (value: Token[]) => void>();
    const read = vi.fn((index: number, receive: (value: Token[]) => void) => {
      receivers.set(index, receive);
      return () => { receivers.delete(index); };
    });
    const ready = vi.fn();
    let scrollport!: HTMLDivElement;
    const view = render(() => <div ref={scrollport}>
      <VirtualMarkdownDocument
        document={{ blocks: Array.from({ length: 20 }, () => ({ chars: 100, lines: 2 })), read, ready }}
        projectId="project"
        scrollport={() => scrollport}
      />
    </div>);
    Object.defineProperties(scrollport, { clientHeight: { value: 200 }, scrollHeight: { value: 2000 } });
    const motion = bindScrollportMotion(scrollport, scrollport, scrollport);
    instance.scrollElement = scrollport;
    const stopOffset = vi.mocked(createVirtualizer).mock.calls[0]![0].observeElementOffset!(instance, vi.fn());
    receivers.get(0)!(tokens(0));
    receivers.get(1)!(tokens(1));
    await waitFor(() => expect(ready).toHaveBeenCalledOnce());
    expect(view.container.textContent).toContain("Section 0");

    setItems([row(10), row(11)]);
    motion.input.beginThumbGesture();
    motion.commit(1000, "thumb_drag");
    expect(view.container.querySelector('[data-retained="true"]')).not.toBeNull();
    expect(view.container.textContent).toContain("Section 0");
    expect((view.container.querySelector('[data-index="0"]') as HTMLElement).style.transform).toBe("translateY(1000px)");
    instance.scrollOffset = 1000;
    expect(instance.shouldAdjustScrollPositionOnItemSizeChange!(row(0), 100, instance)).toBe(false);
    motion.input.endThumbGesture();
    receivers.get(10)!(tokens(10));
    expect(view.container.textContent).not.toContain("Section 10");
    receivers.get(11)!(tokens(11));
    expect(view.container.textContent).toContain("Section 10");
    expect(view.container.textContent).not.toContain("Section 0");

    setItems([row(0), row(1)]);
    motion.commit(0, "thumb_drag");
    expect(view.container.textContent).toContain("Section 0");
    expect(view.container.querySelector('[data-retained="true"]')).toBeNull();
    expect(read.mock.calls.map(([index]) => index)).toEqual([0, 1, 10, 11]);
    setItems([row(0), row(1), row(2)]);
    expect(view.container.querySelector('[data-retained="true"]')).toBeNull();
    expect(view.container.textContent).toContain("Section 0");
    setItems([row(15), row(16)]);
    expect(receivers.size).toBe(2);
    view.unmount();
    expect(receivers.size).toBe(0);
    stopOffset?.();
    unbindScrollportMotion(scrollport);
  });
});
