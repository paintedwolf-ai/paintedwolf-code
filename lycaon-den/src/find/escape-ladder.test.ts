// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  registerEscapeLadderLayer,
  resetEscapeLadderForTests,
  tryConsumeEscapeLadder,
} from "./escape-ladder.ts";
import {
  closeFind,
  findController,
  openFind,
  registerFindableView,
  resetFindControllerForTests,
  setPrimaryFindableView,
} from "./find-controller.ts";

afterEach(() => {
  resetFindControllerForTests();
  resetEscapeLadderForTests();
  // Restore the FindBar slot.
  registerEscapeLadderLayer("findBar", {
    isOpen: () => findController.isOpen(),
    dismiss: () => closeFind(),
  });
  document.body.replaceChildren();
});

describe("escape ladder", () => {
  it("closes exactly the open layer and nothing below it", () => {
    const calls: string[] = [];
    let inlineOpen = true;
    let latchOpen = true;

    registerEscapeLadderLayer("inlineEdit", {
      isOpen: () => inlineOpen,
      dismiss: () => {
        inlineOpen = false;
        calls.push("inlineEdit");
      },
    });
    registerEscapeLadderLayer("stageLatch", {
      isOpen: () => latchOpen,
      dismiss: () => {
        latchOpen = false;
        calls.push("stageLatch");
      },
    });

    const root = document.createElement("div");
    root.textContent = "x";
    document.body.appendChild(root);
    registerFindableView({
      id: "stub",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("stub");
    openFind();
    expect(findController.isOpen()).toBe(true);

    expect(tryConsumeEscapeLadder()).toBe(true);
    expect(calls).toEqual(["inlineEdit"]);
    expect(findController.isOpen()).toBe(true);
    expect(latchOpen).toBe(true);

    expect(tryConsumeEscapeLadder()).toBe(true);
    expect(findController.isOpen()).toBe(false);
    expect(calls).toEqual(["inlineEdit"]);

    expect(tryConsumeEscapeLadder()).toBe(true);
    expect(calls).toEqual(["inlineEdit", "stageLatch"]);

    expect(tryConsumeEscapeLadder()).toBe(false);
  });

  it("skips empty future slots", () => {
    const dismiss = vi.fn();
    registerEscapeLadderLayer("findBar", {
      isOpen: () => true,
      dismiss,
    });
    expect(tryConsumeEscapeLadder()).toBe(true);
    expect(dismiss).toHaveBeenCalledOnce();
  });

  it("gives an Escape to the layer open when the press began, though its own handler closed it", async () => {
    let open = true;
    const dismiss = vi.fn();
    registerEscapeLadderLayer("stageLatch", { isOpen: () => open, dismiss });
    const input = document.createElement("input");
    document.body.append(input);
    input.addEventListener("keydown", () => { open = false; });
    let consumed = false;
    window.addEventListener("keydown", () => { consumed = tryConsumeEscapeLadder(); }, { once: true });

    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(consumed).toBe(true);
    expect(dismiss).toHaveBeenCalledOnce();

    // The next press starts from the layer's own state.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(tryConsumeEscapeLadder()).toBe(false);
  });
});
