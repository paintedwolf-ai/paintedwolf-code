import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { For, Show, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AnchoredSurface } from "./AnchoredSurface.tsx";

const anchorRect = {
  top: 100,
  right: 220,
  bottom: 130,
  left: 100,
  width: 120,
  height: 30,
};

afterEach(cleanup);

describe("AnchoredSurface", () => {
  it("re-places the surface when content changes after mount", async () => {
    const [rows, setRows] = createSignal(["Loading history…"]);
    const onPositioned = vi.fn();
    render(() => (
      <AnchoredSurface anchor={() => anchorRect} onPositioned={onPositioned}>
        <For each={rows()}>{(row) => <div>{row}</div>}</For>
      </AnchoredSurface>
    ));
    await waitFor(() => expect(onPositioned).toHaveBeenCalled());
    const placedCalls = onPositioned.mock.calls.length;

    setRows(["Created", "Edited", "Renamed", "Created"]);
    await waitFor(() =>
      expect(onPositioned.mock.calls.length).toBeGreaterThan(placedCalls),
    );
  });

  it("measures the surface once per placement", async () => {
    const onPositioned = vi.fn();
    let measurements = 0;
    render(() => (
      <AnchoredSurface
        anchor={() => anchorRect}
        onPositioned={onPositioned}
        ref={(element) => {
          const measure = element.getBoundingClientRect.bind(element);
          element.getBoundingClientRect = () => { measurements += 1; return measure(); };
        }}
      >
        <div>Item</div>
      </AnchoredSurface>
    ));
    await waitFor(() => expect(onPositioned).toHaveBeenCalledOnce());
    // Reading the placed box would flush the position writes a second time.
    expect(measurements).toBe(1);
  });

  it("dismisses on scroll only when the scrolled box holds an element anchor", async () => {
    const onDismiss = vi.fn();
    const onPositioned = vi.fn();
    let trigger: HTMLButtonElement | undefined;
    let unrelated: HTMLDivElement | undefined;
    let scrollContainer: HTMLDivElement | undefined;
    render(() => (
      <>
        <div ref={unrelated} data-testid="transcript" />
        <div ref={scrollContainer}>
          <button ref={trigger} type="button">Runs</button>
        </div>
        <AnchoredSurface
          anchor={() => trigger}
          dismissOnScroll
          onDismiss={onDismiss}
          onPositioned={onPositioned}
        >
          <div>Run list</div>
        </AnchoredSurface>
      </>
    ));
    await waitFor(() => expect(onPositioned).toHaveBeenCalled());

    fireEvent.scroll(unrelated!);
    expect(onDismiss).not.toHaveBeenCalled();

    fireEvent.scroll(scrollContainer!);
    expect(onDismiss).toHaveBeenCalledTimes(1);

    fireEvent.scroll(document);
    expect(onDismiss).toHaveBeenCalledTimes(2);
  });

  it("dismisses a point anchor only when the scrolled box covers the point", async () => {
    const onDismiss = vi.fn();
    const onPositioned = vi.fn();
    let far: HTMLDivElement | undefined;
    let under: HTMLDivElement | undefined;
    render(() => (
      <>
        <div ref={far} />
        <div ref={under} />
        <AnchoredSurface
          anchor={() => ({ x: 150, y: 115 })}
          dismissOnScroll
          onDismiss={onDismiss}
          onPositioned={onPositioned}
        >
          <div>Menu</div>
        </AnchoredSurface>
      </>
    ));
    await waitFor(() => expect(onPositioned).toHaveBeenCalled());
    const box = (rect: Partial<DOMRect>) => () => ({
      x: 0, y: 0, width: 0, height: 0, top: 0, right: 0, bottom: 0, left: 0,
      toJSON: () => ({}),
      ...rect,
    }) as DOMRect;
    far!.getBoundingClientRect = box({ top: 400, bottom: 800, left: 400, right: 800 });
    under!.getBoundingClientRect = box({ top: 100, bottom: 130, left: 100, right: 220 });

    fireEvent.scroll(far!);
    expect(onDismiss).not.toHaveBeenCalled();

    fireEvent.scroll(under!);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("dismisses an opted-in surface when the anchor's pane stops being presented", async () => {
    const onDismiss = vi.fn();
    const keptOpen = vi.fn();
    const onPositioned = vi.fn();
    const [collapsed, setCollapsed] = createSignal(false);
    let trigger: HTMLButtonElement | undefined;
    let menuTrigger: HTMLButtonElement | undefined;
    render(() => (
      <>
        <aside aria-hidden={collapsed()} inert={collapsed() ? true : undefined}>
          <button ref={trigger} type="button">Hide sidebar</button>
          <button ref={menuTrigger} type="button">Actions</button>
        </aside>
        <AnchoredSurface
          anchor={() => trigger}
          dismissWhenAnchorHidden
          onDismiss={onDismiss}
          onPositioned={onPositioned}
        >
          <div>Hide sidebar</div>
        </AnchoredSurface>
        <AnchoredSurface anchor={() => menuTrigger} onDismiss={keptOpen}>
          <div>Actions</div>
        </AnchoredSurface>
      </>
    ));
    await waitFor(() => expect(onPositioned).toHaveBeenCalled());
    expect(onDismiss).not.toHaveBeenCalled();

    setCollapsed(true);
    await waitFor(() => expect(onDismiss).toHaveBeenCalled());
    // The control is still mounted: only its pane went away.
    expect(trigger?.isConnected).toBe(true);
    expect(keptOpen).not.toHaveBeenCalled();
  });

  it("focuses and dismisses a menu with custom navigation", async () => {
    const [open, setOpen] = createSignal(true);
    const navigate = vi.fn();
    let trigger: HTMLButtonElement | undefined;
    render(() => <>
      <button ref={trigger}>History</button>
      <Show when={open()}><AnchoredSurface anchor={() => trigger} role="menu" ariaLabel="History"
        onKeyDown={navigate} onDismiss={() => setOpen(false)}>
        <button role="menuitemradio">Current</button>
      </AnchoredSurface></Show>
    </>);
    const item = screen.getByRole("menuitemradio");
    await waitFor(() => expect(document.activeElement).toBe(item));
    fireEvent.keyDown(item, { key: "ArrowDown" });
    expect(navigate).toHaveBeenCalledOnce();
    fireEvent.keyDown(item, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });

  it("focuses and navigates menu items, then restores trigger focus on Escape", async () => {
    const [open, setOpen] = createSignal(true);
    const focusOptions: FocusOptions[] = [];
    const focus = HTMLElement.prototype.focus;
    HTMLElement.prototype.focus = function (options?: FocusOptions) {
      focusOptions.push(options ?? {});
      return focus.call(this, options);
    };
    let trigger: HTMLButtonElement | undefined;
    try {
      render(() => (
        <>
          <button ref={trigger} type="button">Actions</button>
          <Show when={open()}>
            <AnchoredSurface
              anchor={() => trigger}
              role="menu"
              ariaLabel="Actions"
              onDismiss={() => setOpen(false)}
            >
              <button type="button" role="menuitem">Open</button>
              <button type="button" role="menuitem" disabled>Unavailable</button>
              <button type="button" role="menuitem">Rename</button>
              <button type="button" role="menuitem">Delete</button>
            </AnchoredSurface>
          </Show>
        </>
      ));

      const openItem = await screen.findByRole("menuitem", { name: "Open" });
      const renameItem = screen.getByRole("menuitem", { name: "Rename" });
      const deleteItem = screen.getByRole("menuitem", { name: "Delete" });
      await waitFor(() => expect(document.activeElement).toBe(openItem));

      fireEvent.keyDown(openItem, { key: "ArrowDown" });
      expect(document.activeElement).toBe(renameItem);
      fireEvent.keyDown(renameItem, { key: "End" });
      expect(document.activeElement).toBe(deleteItem);
      fireEvent.keyDown(deleteItem, { key: "o" });
      expect(document.activeElement).toBe(openItem);

      fireEvent.keyDown(openItem, { key: "Escape" });
      await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
      expect(document.activeElement).toBe(trigger);
      expect(focusOptions.length).toBeGreaterThan(0);
      expect(focusOptions.every((options) => options.preventScroll === true)).toBe(true);
    } finally {
      HTMLElement.prototype.focus = focus;
    }
  });
});
