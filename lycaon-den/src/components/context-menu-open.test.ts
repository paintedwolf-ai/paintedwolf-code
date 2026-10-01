// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { bindChromeContextMenu, overflowMenuAnchor } from "./context-menu-open.ts";

describe("bindChromeContextMenu", () => {
  it("opens at the pointer and stops the event", () => {
    const open = vi.fn();
    const bind = bindChromeContextMenu(open);
    const btn = document.createElement("button");
    document.body.append(btn);
    const event = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: 40,
      clientY: 80,
    });
    Object.defineProperty(event, "currentTarget", { value: btn });
    bind.onContextMenu(event);
    expect(event.defaultPrevented).toBe(true);
    expect(event.cancelBubble).toBe(true);
    expect(open).toHaveBeenCalledWith({ x: 40, y: 80 });
    btn.remove();
  });

  it("does not open from a disabled button", () => {
    const open = vi.fn();
    const bind = bindChromeContextMenu(open);
    const btn = document.createElement("button");
    btn.disabled = true;
    document.body.append(btn);
    const event = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: 1,
      clientY: 1,
    });
    Object.defineProperty(event, "currentTarget", { value: btn });
    bind.onContextMenu(event);
    expect(event.defaultPrevented).toBe(true);
    expect(open).not.toHaveBeenCalled();
    btn.remove();
  });

  it("opens from Shift+F10 at the control's box", () => {
    const open = vi.fn();
    const bind = bindChromeContextMenu(open);
    const btn = document.createElement("button");
    document.body.append(btn);
    vi.spyOn(btn, "getBoundingClientRect").mockReturnValue({
      left: 10,
      bottom: 30,
      right: 20,
      top: 12,
      width: 10,
      height: 18,
      x: 10,
      y: 12,
      toJSON: () => ({}),
    });
    const event = new KeyboardEvent("keydown", {
      key: "F10",
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(event, "currentTarget", { value: btn });
    bind.onKeyDown(event);
    expect(event.defaultPrevented).toBe(true);
    expect(open).toHaveBeenCalledWith({ x: 10, y: 30 });
    btn.remove();
  });
});

describe("overflowMenuAnchor", () => {
  it("anchors at the trigger's right edge", () => {
    const btn = document.createElement("button");
    document.body.append(btn);
    vi.spyOn(btn, "getBoundingClientRect").mockReturnValue({
      left: 10,
      bottom: 30,
      right: 40,
      top: 12,
      width: 30,
      height: 18,
      x: 10,
      y: 12,
      toJSON: () => ({}),
    });
    expect(overflowMenuAnchor(btn)).toEqual({ x: 40, y: 36 });
    expect(overflowMenuAnchor(null)).toBeNull();
    btn.remove();
  });
});
