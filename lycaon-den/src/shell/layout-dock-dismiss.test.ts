// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { dismissLayoutDockOnOutsidePress } from "./layout-dock-dismiss.ts";

const cleanups: Array<() => void> = [];

afterEach(() => {
  while (cleanups.length) cleanups.pop()?.();
  document.body.innerHTML = "";
});

function el(id: string): HTMLElement {
  const node = document.createElement("div");
  node.id = id;
  node.append(document.createElement("span"));
  document.body.append(node);
  return node;
}

/** jsdom has no PointerEvent; the listener only reads `target`. */
function press(target: Node): void {
  target.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
}

function focus(target: HTMLElement): void {
  target.tabIndex = -1;
  target.focus();
}

function mount(open: () => boolean, dock: HTMLElement) {
  const onDismiss = vi.fn();
  createRoot((dispose) => {
    cleanups.push(dispose);
    dismissLayoutDockOnOutsidePress({
      open,
      dock: () => dock,
      onDismiss,
    });
  });
  return onDismiss;
}

describe("dismissLayoutDockOnOutsidePress", () => {
  it("keeps Layout open for interactions anywhere in its dock", () => {
    const dock = el("dock");
    const onDismiss = mount(() => true, dock);
    press(dock.firstChild!);
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("retires Layout when a press lands outside its dock", () => {
    const dock = el("dock");
    const stage = el("stage");
    const onDismiss = mount(() => true, dock);
    press(stage);
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("dismisses without consuming another interactive target's click", () => {
    const dock = el("dock");
    const target = document.createElement("button");
    const onTargetClick = vi.fn();
    target.addEventListener("click", onTargetClick);
    document.body.append(target);
    const onDismiss = mount(() => true, dock);

    press(target);
    target.click();

    expect(onDismiss).toHaveBeenCalledOnce();
    expect(onTargetClick).toHaveBeenCalledOnce();
  });

  it("retires Layout when keyboard focus enters another surface", () => {
    const dock = el("dock");
    const stage = el("stage");
    const onDismiss = mount(() => true, dock);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    focus(stage);
    document.dispatchEvent(new KeyboardEvent("keyup", { key: "Tab", bubbles: true }));
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("keeps Layout open when the restored stage focuses itself", () => {
    const dock = el("dock");
    const stage = el("stage");
    const [open, setOpen] = createSignal(false);
    const onDismiss = mount(open, dock);
    press(dock);
    setOpen(true);
    focus(stage);
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("does not treat later automatic focus as keyboard navigation", () => {
    const dock = el("dock");
    const stage = el("stage");
    const onDismiss = mount(() => true, dock);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    focus(dock);
    document.dispatchEvent(new KeyboardEvent("keyup", { key: "Tab", bubbles: true }));
    focus(stage);
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("listens only while Layout is open", () => {
    const [open, setOpen] = createSignal(false);
    const dock = el("dock");
    const stage = el("stage");
    const onDismiss = mount(open, dock);
    press(stage);
    expect(onDismiss).not.toHaveBeenCalled();

    setOpen(true);
    press(stage);
    expect(onDismiss).toHaveBeenCalledTimes(1);

    setOpen(false);
    press(stage);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
