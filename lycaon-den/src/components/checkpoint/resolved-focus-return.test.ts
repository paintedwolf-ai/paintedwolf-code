// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createResolvedFocusReturn } from "./resolved-focus-return.ts";

afterEach(() => document.body.replaceChildren());

function focusedButton(): HTMLButtonElement {
  const button = document.createElement("button");
  document.body.append(button);
  button.focus();
  return button;
}

describe("createResolvedFocusReturn", () => {
  it("waits while the resolving control is still usable, then returns focus once it leaves", () => {
    const returnFocus = vi.fn();
    const focusReturn = createResolvedFocusReturn(returnFocus);
    const button = focusedButton();
    focusReturn.note();
    focusReturn.settle();
    expect(returnFocus).not.toHaveBeenCalled();
    button.remove();
    focusReturn.settle();
    focusReturn.settle();
    expect(returnFocus).toHaveBeenCalledOnce();
  });

  it("blurs a control stranded inside an inert card before returning focus", () => {
    const returnFocus = vi.fn();
    const focusReturn = createResolvedFocusReturn(returnFocus);
    const card = document.createElement("div");
    const button = document.createElement("button");
    card.append(button);
    document.body.append(card);
    button.focus();
    focusReturn.note();
    card.setAttribute("inert", "");
    focusReturn.settle();
    expect(document.activeElement).toBe(document.body);
    expect(returnFocus).toHaveBeenCalledOnce();
  });

  it("waits while a closed menu hands focus back into the resolving card, then returns it when the card retires", () => {
    const returnFocus = vi.fn();
    const focusReturn = createResolvedFocusReturn(returnFocus);
    const card = document.createElement("article");
    card.setAttribute("data-checkpoint-id", "c1");
    const trigger = document.createElement("button");
    card.append(trigger);
    document.body.append(card);
    const item = focusedButton();
    focusReturn.note("c1");
    item.remove();
    trigger.focus();
    focusReturn.settle();
    expect(returnFocus).not.toHaveBeenCalled();
    card.setAttribute("inert", "");
    focusReturn.settle();
    expect(document.activeElement).toBe(document.body);
    expect(returnFocus).toHaveBeenCalledOnce();
  });

  it("leaves focus that moved into a different card", () => {
    const returnFocus = vi.fn();
    const focusReturn = createResolvedFocusReturn(returnFocus);
    const other = document.createElement("article");
    other.setAttribute("data-checkpoint-id", "c2");
    const control = document.createElement("button");
    other.append(control);
    document.body.append(other);
    const item = focusedButton();
    focusReturn.note("c1");
    item.remove();
    control.focus();
    focusReturn.settle();
    expect(returnFocus).not.toHaveBeenCalled();
  });

  it("leaves focus the person moved elsewhere and ignores resolutions made without focus", () => {
    const returnFocus = vi.fn();
    const focusReturn = createResolvedFocusReturn(returnFocus);
    const button = focusedButton();
    focusReturn.note();
    const other = focusedButton();
    button.remove();
    focusReturn.settle();
    other.blur();
    focusReturn.note();
    focusReturn.settle();
    expect(returnFocus).not.toHaveBeenCalled();
  });
});
