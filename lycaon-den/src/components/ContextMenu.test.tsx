import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../ui/resident-presence-context.tsx";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { ContextMenu } from "./ContextMenu.tsx";

describe("ContextMenu", () => {
  it("releases keyboard interception while its retained tab is idle", async () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    const onSelect = vi.fn();
    const onDismiss = vi.fn();
    render(() => <ResidentPresenceProvider presence={presence()}>
      <ContextMenu anchor={input} retainFocus onDismiss={onDismiss}
        items={[{ label: "Choose", onSelect }]} />
    </ResidentPresenceProvider>);
    await Promise.resolve();
    setPresence("idle");
    fireEvent.keyDown(document, { key: "Enter" });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onSelect).not.toHaveBeenCalled();
    expect(onDismiss).not.toHaveBeenCalled();
    setPresence("active");
    await Promise.resolve();
    fireEvent.keyDown(document, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledOnce();
    input.remove();
  });

  it("uses the themed overflow host", () => {
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[{ label: "Open", onSelect: vi.fn() }]}
      />
    ));
    expect(screen.getByRole("menu").getAttribute("data-den-scrollport")).toBe("y");
    expect(
      screen.getByRole("menu").querySelector(
        ":scope > .den-scrollport__viewport > .den-scrollport__content.den-context-menu__content",
      ),
    ).toBeTruthy();
  });

  it("keeps shortcut hints separate from labels without changing keyboard activation", async () => {
    const onSelect = vi.fn();
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[{ label: "Add to chat", shortcut: "⌘L", onSelect }]}
      />
    ));
    await Promise.resolve();
    const item = screen.getByRole("menuitem", { name: "Add to chat" });
    expect(item.querySelector(".den-context-menu__label")?.textContent).toBe("Add to chat");
    expect(item.querySelector(".den-context-menu__shortcut")?.textContent).toBe("⌘L");
    fireEvent.keyDown(item, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledOnce();
  });

  it("invokes an item's handler then dismisses", () => {
    const onSelect = vi.fn();
    const onDismiss = vi.fn();
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={onDismiss}
        items={[{ label: "Open", testId: "item-open", onSelect }]}
      />
    ));
    fireEvent.click(screen.getByTestId("item-open"));
    expect(onSelect).toHaveBeenCalledOnce();
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("dismisses on Escape and on outside pointerdown", () => {
    const onDismiss = vi.fn();
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={onDismiss}
        items={[{ label: "Open", onSelect: vi.fn() }]}
      />
    ));

    fireEvent.keyDown(document, { key: "Escape" });
    expect(onDismiss).toHaveBeenCalledTimes(1);

    fireEvent.pointerDown(document.body);
    expect(onDismiss).toHaveBeenCalledTimes(2);
  });

  it("does not dismiss when pointerdown stays inside the menu", () => {
    const onDismiss = vi.fn();
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={onDismiss}
        items={[{ label: "Open", testId: "item-open", onSelect: vi.fn() }]}
      />
    ));
    fireEvent.pointerDown(screen.getByTestId("item-open"));
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("does not clear a live text selection when the menu mounts and focuses", async () => {
    const prose = document.createElement("p");
    prose.textContent = "keep me selected";
    document.body.appendChild(prose);
    const range = document.createRange();
    range.selectNodeContents(prose);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
    expect(selection?.toString()).toBe("keep me selected");

    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[{ label: "Copy", testId: "item-copy", onSelect: vi.fn() }]}
      />
    ));

    // Autofocus runs in a microtask, which is where the selection collapses.
    await Promise.resolve();
    expect(document.activeElement).toBe(screen.getByTestId("item-copy"));
    expect(window.getSelection()?.toString()).toBe("keep me selected");
    prose.remove();
  });

  it("prevents mousedown default so clicking an item keeps the selection", () => {
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[{ label: "Copy", testId: "item-copy", onSelect: vi.fn() }]}
      />
    ));
    const item = screen.getByTestId("item-copy");
    const event = new MouseEvent("mousedown", {
      bubbles: true,
      cancelable: true,
    });
    item.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
  });

  it("exposes a disabled action reason without changing its accessible name", () => {
    render(() => <ContextMenu anchor={{ x: 10, y: 10 }} onDismiss={vi.fn()}
      items={[{ label: "External editor", disabled: true, description: "This file is not present on disk." }]} />);
    const item = screen.getByRole("menuitem", { name: "External editor" });
    expect(item.getAttribute("aria-disabled")).toBe("true");
    const description = document.getElementById(item.getAttribute("aria-describedby")!);
    expect(description?.textContent).toBe("This file is not present on disk.");
  });

  it("renders a descriptive target header", () => {
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[
          {
            label: "resolveProjectRoot",
            kicker: "Symbol actions",
            description: "Symbol under pointer · no selection needed",
            header: true,
            testId: "symbol-header",
          },
          { label: "Rename", onSelect: vi.fn() },
        ]}
      />
    ));
    const header = screen.getByTestId("symbol-header");
    expect(header.textContent).toContain("Symbol actions");
    expect(header.textContent).toContain("resolveProjectRoot");
    expect(header.textContent).toContain("no selection needed");
    expect(screen.getByRole("menu").getAttribute("aria-label")).toBe(
      "Symbol actions. resolveProjectRoot. Symbol under pointer · no selection needed",
    );
  });

  it("moves keyboard focus across headers and separators by item identity", async () => {
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[
          { label: "Copy", testId: "item-copy", onSelect: vi.fn() },
          { separator: true },
          {
            label: "Symbol actions",
            header: true,
          },
          { label: "Rename", testId: "item-rename", onSelect: vi.fn() },
        ]}
      />
    ));
    await Promise.resolve();
    expect(document.activeElement).toBe(screen.getByTestId("item-copy"));
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(screen.getByTestId("item-rename"));
  });

  it("opens a submenu by pointer and dismisses the whole menu after selection", () => {
    const onSelect = vi.fn();
    const onDismiss = vi.fn();
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={onDismiss}
        items={[
          {
            label: "Copy",
            testId: "copy-menu",
            submenu: [
              {
                label: "Copy path",
                testId: "copy-path",
                onSelect,
              },
            ],
          },
        ]}
      />
    ));

    fireEvent.mouseEnter(screen.getByTestId("copy-menu"));
    expect(screen.getByRole("menu", { name: "Copy submenu" })).toBeTruthy();
    fireEvent.click(screen.getByTestId("copy-path"));
    expect(onSelect).toHaveBeenCalledOnce();
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("moves into and back out of a submenu with desktop menu keys", async () => {
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[
          {
            label: "Ask about selection",
            testId: "ask-menu",
            submenu: [
              {
                label: "Explain in context",
                testId: "ask-explain",
                onSelect: vi.fn(),
              },
            ],
          },
        ]}
      />
    ));
    await Promise.resolve();
    const parent = screen.getByTestId("ask-menu");
    expect(document.activeElement).toBe(parent);

    fireEvent.keyDown(parent, { key: "ArrowRight" });
    await Promise.resolve();
    const child = screen.getByTestId("ask-explain");
    expect(document.activeElement).toBe(child);

    fireEvent.keyDown(child, { key: "ArrowLeft" });
    await Promise.resolve();
    expect(document.activeElement).toBe(parent);
    expect(screen.queryByTestId("ask-explain")).toBeNull();
  });

  it("leaves a field focused and selected when opened with retainFocus", async () => {
    const field = document.createElement("textarea");
    field.value = "keep me selected";
    document.body.appendChild(field);
    field.focus();
    field.setSelectionRange(0, 7);

    const { unmount } = render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        retainFocus
        onDismiss={vi.fn()}
        items={[
          { label: "Copy", testId: "item-copy", onSelect: vi.fn() },
          { label: "Cut", testId: "item-cut", onSelect: vi.fn() },
        ]}
      />
    ));
    await Promise.resolve();

    expect(document.activeElement).toBe(field);
    expect([field.selectionStart, field.selectionEnd]).toEqual([0, 7]);
    expect(screen.getByTestId("item-copy").classList).toContain(
      "den-context-menu__item--virtual-focus",
    );

    // Arrow keys drive the highlight from the document, not from menu focus.
    fireEvent.keyDown(document, { key: "ArrowDown" });
    expect(document.activeElement).toBe(field);
    expect(screen.getByTestId("item-cut").classList).toContain(
      "den-context-menu__item--virtual-focus",
    );
    expect(screen.getByTestId("item-copy").classList).not.toContain(
      "den-context-menu__item--virtual-focus",
    );

    unmount();
    field.remove();
  });

  it("leaves a reader's selection in place when opened with retainFocus", async () => {
    const prose = document.createElement("p");
    const text = document.createTextNode("reader selection");
    prose.append(text);
    document.body.append(prose);
    const selection = window.getSelection();
    const range = document.createRange();
    range.setStart(text, 0);
    range.setEnd(text, 6);
    selection?.removeAllRanges();
    selection?.addRange(range);
    const wasActive = document.activeElement;

    const { unmount } = render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        retainFocus
        onDismiss={vi.fn()}
        items={[
          { label: "Copy", testId: "item-copy", onSelect: vi.fn() },
          { label: "Add to chat", testId: "item-add", onSelect: vi.fn() },
        ]}
      />
    ));
    await Promise.resolve();

    expect(document.activeElement).toBe(wasActive);
    expect(selection?.toString()).toBe("reader");
    expect(screen.getByTestId("item-copy").classList).toContain(
      "den-context-menu__item--virtual-focus",
    );

    fireEvent.keyDown(document, { key: "ArrowDown" });
    expect(screen.getByTestId("item-add").classList).toContain(
      "den-context-menu__item--virtual-focus",
    );
    expect(selection?.toString()).toBe("reader");

    unmount();
    prose.remove();
  });

  it("hands a field's focus and selection back when the menu closes", async () => {
    const field = document.createElement("input");
    field.type = "text";
    field.value = "hand it back";
    document.body.appendChild(field);
    field.focus();
    field.setSelectionRange(0, 4);

    const { unmount } = render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={vi.fn()}
        items={[{ label: "Copy", testId: "item-copy", onSelect: vi.fn() }]}
      />
    ));
    await Promise.resolve();
    expect(document.activeElement).toBe(screen.getByTestId("item-copy"));

    unmount();
    await Promise.resolve();
    expect(document.activeElement).toBe(field);
    expect([field.selectionStart, field.selectionEnd]).toEqual([0, 4]);
    field.remove();
  });

  it("returns focus without undoing a selection made by the action", async () => {
    const trigger = document.createElement("button");
    const prose = document.createElement("p");
    const text = document.createTextNode("select all of this");
    prose.append(text);
    document.body.append(trigger, prose);
    trigger.focus();
    const selection = window.getSelection();
    const range = document.createRange();
    range.setStart(text, 0);
    range.setEnd(text, 6);
    selection?.removeAllRanges();
    selection?.addRange(range);
    const { unmount } = render(() => <ContextMenu anchor={trigger}
      onDismiss={() => unmount()} items={[{
        label: "Select all", onSelect: () => {
          const all = document.createRange();
          all.selectNodeContents(prose);
          selection?.removeAllRanges();
          selection?.addRange(all);
        },
      }]} />);
    await Promise.resolve();
    fireEvent.click(screen.getByRole("menuitem", { name: "Select all" }));
    await Promise.resolve();
    expect(document.activeElement).toBe(trigger);
    expect(selection?.toString()).toBe("select all of this");
    trigger.remove();
    prose.remove();
  });
});
