// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { activateFocusTrap, focusableElements } from "./focus-trap.ts";

function button(label: string): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.textContent = label;
  return b;
}

describe("activateFocusTrap", () => {
  it("focuses the first focusable and cycles Tab / Shift+Tab", async () => {
    const root = document.createElement("div");
    const a = button("A");
    const b = button("B");
    const c = button("C");
    root.append(a, b, c);
    document.body.append(root);

    const outside = button("outside");
    document.body.append(outside);
    outside.focus();

    const trap = activateFocusTrap(root);
    await Promise.resolve();
    expect(document.activeElement).toBe(a);

    a.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }),
    );
    // Synthetic Tab events need explicit focus movement.
    c.focus();
    const tab = new KeyboardEvent("keydown", {
      key: "Tab",
      bubbles: true,
      cancelable: true,
    });
    root.dispatchEvent(tab);
    expect(tab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(a);

    a.focus();
    const shiftTab = new KeyboardEvent("keydown", {
      key: "Tab",
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    root.dispatchEvent(shiftTab);
    expect(shiftTab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(c);

    trap.deactivate();
    expect(document.activeElement).toBe(outside);

    root.remove();
    outside.remove();
  });

  it("leaves focus at the destination when closed before activation runs", async () => {
    const root = document.createElement("div");
    root.append(button("dialog control"));
    const destination = button("destination");
    document.body.append(root, destination);

    try {
      const trap = activateFocusTrap(root);
      trap.deactivate({ restoreFocus: false });
      destination.focus();
      await Promise.resolve();

      expect(document.activeElement).toBe(destination);
    } finally {
      root.remove();
      destination.remove();
    }
  });

  it("restores prior focus and clears inert on deactivate", async () => {
    const shell = document.createElement("div");
    shell.id = "shell";
    const opener = button("opener");
    shell.append(opener);
    document.body.append(shell);
    opener.focus();

    const dialog = document.createElement("div");
    dialog.append(button("close"));
    document.body.append(dialog);

    const trap = activateFocusTrap(dialog, { inertTarget: shell });
    await Promise.resolve();
    expect(shell.hasAttribute("inert")).toBe(true);

    trap.deactivate();
    expect(shell.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(opener);

    dialog.remove();
    shell.remove();
  });

  it("can relinquish focus restoration to a destination surface", async () => {
    const opener = button("opener");
    const destination = button("destination");
    document.body.append(opener, destination);
    opener.focus();

    const dialog = document.createElement("div");
    dialog.append(button("close"));
    document.body.append(dialog);

    const trap = activateFocusTrap(dialog);
    await Promise.resolve();
    destination.focus();
    trap.deactivate({ restoreFocus: false });

    expect(document.activeElement).toBe(destination);
    dialog.remove();
    opener.remove();
    destination.remove();
  });

  it("keeps shared and pre-existing inert state until its final trap releases", () => {
    const shell = document.createElement("div");
    const firstDialog = document.createElement("div");
    const secondDialog = document.createElement("div");
    const first = activateFocusTrap(firstDialog, {
      inertTarget: shell,
      autoFocus: false,
    });
    const second = activateFocusTrap(secondDialog, {
      inertTarget: shell,
      autoFocus: false,
    });

    first.deactivate();
    expect(shell.hasAttribute("inert")).toBe(true);
    second.deactivate();
    expect(shell.hasAttribute("inert")).toBe(false);

    shell.setAttribute("inert", "");
    const preserving = activateFocusTrap(firstDialog, {
      inertTarget: shell,
      autoFocus: false,
    });
    preserving.deactivate();
    expect(shell.hasAttribute("inert")).toBe(true);
  });

  it("calls onEscape when provided (opt-in; Shell overlays use overlay.dismiss)", () => {
    const root = document.createElement("div");
    root.append(button("x"));
    document.body.append(root);
    const onEscape = vi.fn();
    const trap = activateFocusTrap(root, { onEscape });
    const esc = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    });
    root.dispatchEvent(esc);
    expect(onEscape).toHaveBeenCalledTimes(1);
    expect(esc.defaultPrevented).toBe(true);
    trap.deactivate();
    root.remove();
  });

  it("lists focusable controls", () => {
    const root = document.createElement("div");
    const enabled = button("enabled");
    const skipped = button("skipped");
    skipped.tabIndex = -1;
    const disabled = button("disabled");
    disabled.disabled = true;
    const editable = document.createElement("div");
    editable.setAttribute("contenteditable", "true");
    const hidden = document.createElement("div");
    hidden.setAttribute("aria-hidden", "true");
    hidden.append(button("hidden"));
    root.append(enabled, skipped, disabled, editable, hidden);

    expect(focusableElements(root)).toEqual([enabled, editable]);
  });
});
