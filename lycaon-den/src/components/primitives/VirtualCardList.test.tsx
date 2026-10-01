import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { VirtualCardList } from "./VirtualCardList.tsx";
import type { SharedResizeListener } from "../../layout/shared-resize-observer.ts";

const measurements = vi.hoisted(() => new Map<Element, SharedResizeListener>());
vi.mock("../../layout/shared-resize-observer.ts", () => ({
  observeSharedContentBox: (element: Element, listener: SharedResizeListener) => {
    measurements.set(element,listener);
    return () => measurements.delete(element);
  },
}));

describe("bounded card lists", () => {
  // jsdom has no scrolling; a bounded list writes its viewport natively until its scrollbar binds.
  beforeAll(() => {
    HTMLElement.prototype.scrollTo ??= function scrollTo() {};
  });

  it("uses the enclosing scroll viewport for long transcript lists", async () => {
    const view = render(() => <div ref={element => { element.scrollTo = vi.fn(); }} style={{ height: "400px", "overflow-y": "auto" }} data-testid="transcript">
      <VirtualCardList scroll="ancestor" items={Array.from({ length: 10000 }, (_, i) => String(i))} keyOf={item => item} label="Actions">
        {item => <span data-row={item()}>{item()}</span>}
      </VirtualCardList>
    </div>);
    const list = view.getByTestId("virtual-card-list");
    expect(list.style.height).toBe("");
    expect(list.style.overflow).toBe("");
    expect(list.closest("[data-den-scrollport]")).toBeNull();
    const scroller = view.getByTestId("transcript");
    scroller.scrollTop = 4800;
    fireEvent.scroll(scroller);
    await waitFor(() => expect(view.container.querySelector('[data-row="100"]')).not.toBeNull());
    // One viewport of rows plus twelve rows of overscan either side, out of ten thousand.
    expect(view.container.querySelectorAll("[data-row]").length).toBeLessThan(60);
    expect(list.scrollTop).toBe(0);
  });
  it("keeps a row mounted while the window slides past it", async () => {
    const mounts = new Map<string, number>();
    const view = render(() => <div ref={element => { element.scrollTo = vi.fn(); }} style={{ height: "400px", "overflow-y": "auto" }} data-testid="page">
      <VirtualCardList scroll="ancestor" items={Array.from({ length: 1000 }, (_, i) => String(i))} keyOf={item => item} label="Rows" estimateSize={48}>
        {item => { mounts.set(item(), (mounts.get(item()) ?? 0) + 1); return <span data-row={item()}>{item()}</span>; }}
      </VirtualCardList>
    </div>);
    const scroller = view.getByTestId("page");
    await waitFor(() => expect(view.container.querySelector('[data-row="10"]')).not.toBeNull());
    const held = view.container.querySelector('[data-row="10"]');
    // Row 20 at the top keeps row 10 inside the twelve-row overscan while row 0 leaves it.
    scroller.scrollTop = 20 * 48;
    fireEvent.scroll(scroller);
    await waitFor(() => expect(view.container.querySelector('[data-row="0"]')).toBeNull());
    expect(view.container.querySelector('[data-row="10"]')).toBe(held);
    expect(mounts.get("10")).toBe(1);
  });
  it("sizes rows it has not measured from their own estimate", async () => {
    const view = render(() => <VirtualCardList items={Array.from({ length: 100 }, (_, i) => i)} keyOf={item => String(item)} label="Rows"
      estimateSize={item => (item === 0 ? 500 : 20)}>{item => <span>{item()}</span>}</VirtualCardList>);
    const second = view.container.querySelector('[data-index="1"]') as HTMLElement;
    await waitFor(() => expect(second.style.transform).toBe("translateY(500px)"));
  });
  it("prepares a bounded number of rows for a large collapsed list", () => {
    const items = Array.from({ length: 10000 }, (_, i) => ({ id: String(i) }));
    const view = render(() => <details><summary>Actions</summary><VirtualCardList items={items} keyOf={item => item.id} label="Actions">{item => <span data-row={item().id}>{item().id}</span>}</VirtualCardList></details>);
    const rows = view.container.querySelectorAll("[data-row]");
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(30);
  });
  it("keeps short lists in flow", () => {
    const view = render(() => <VirtualCardList items={["one", "two"]} keyOf={item => item} label="Actions">{item => <span>{item()}</span>}</VirtualCardList>);
    expect(view.queryByTestId("virtual-card-list")).toBeNull();
    expect(view.getByText("one")).toBeTruthy();
    expect(view.getByText("two")).toBeTruthy();
  });
  it("keeps lists below ancestor threshold in flow when scrolling with ancestor", () => {
    const items = Array.from({ length: 50 }, (_, i) => String(i));
    const view = render(() => (
      <VirtualCardList scroll="ancestor" items={items} keyOf={item => item} label="Actions">
        {item => <span data-row={item()}>{item()}</span>}
      </VirtualCardList>
    ));
    expect(view.queryByTestId("virtual-card-list")).toBeNull();
    expect(view.container.querySelectorAll("[data-row]")).toHaveLength(50);
  });
  it("moves following cards when an expanded card grows and ignores hidden geometry", async () => {
    const view=render(()=><VirtualCardList items={Array.from({length:1000},(_,i)=>String(i))} keyOf={item=>item} label="Actions">{item=><span>{item()}</span>}</VirtualCardList>);
    const first=view.container.querySelector('[data-index="0"]')!;
    const second=view.container.querySelector('[data-index="1"]')!;
    measurements.get(first)!({width:640,height:240,borderHeight:240});
    await waitFor(()=>expect((second as HTMLElement).style.transform).toBe("translateY(240px)"));
    measurements.get(first)!({width:0,height:0,borderHeight:0});
    expect((second as HTMLElement).style.transform).toBe("translateY(240px)");
    view.unmount();
    expect(measurements.has(first)).toBe(false);
  });
  it("holds a bounded list in a scrollport frame, the viewport scrolling the rows", () => {
    const view = render(() => <VirtualCardList items={Array.from({ length: 100 }, (_, i) => String(i))} keyOf={item => item} label="Rows">{item => <span>{item()}</span>}</VirtualCardList>);
    const viewport = view.getByTestId("virtual-card-list");
    expect(viewport.classList.contains("den-scrollport__viewport")).toBe(true);
    expect(viewport.getAttribute("aria-label")).toBe("Rows");
    expect(viewport.tabIndex).toBe(0);
    const frame = viewport.parentElement!;
    expect(frame.getAttribute("data-den-scrollport")).toBe("y");
    expect(frame.style.height).toBe("min(26rem, 50vh)");
    expect(viewport.querySelector('[data-index="0"]')).not.toBeNull();
  });
});
