// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createResolvedFocusReturn } from "./resolved-focus-return.ts";

afterEach(() => document.body.replaceChildren());

function card(id: string): { card: HTMLElement; control: HTMLButtonElement } {
  const element = document.createElement("article");
  element.setAttribute("data-checkpoint-id", id);
  const control = document.createElement("button");
  element.append(control);
  document.body.append(element);
  return { card: element, control };
}

function composer() {
  const textarea = document.createElement("textarea");
  document.body.append(textarea);
  return { textarea, returnFocus: vi.fn(() => textarea.focus()) };
}

describe("createResolvedFocusReturn", () => {
  it("waits while the answer button holds focus in the live card, then returns focus when the card retires", () => {
    const { textarea, returnFocus } = composer();
    const { card: element, control } = card("c1");
    const focusReturn = createResolvedFocusReturn(returnFocus);
    control.focus();
    focusReturn.note("c1");
    focusReturn.settle();
    expect(returnFocus).not.toHaveBeenCalled();
    element.setAttribute("inert", "");
    focusReturn.settle(true);
    focusReturn.settle(true);
    expect(returnFocus).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(textarea);
  });

  it("returns focus as soon as the resolving control goes away, and again if the closing card pulls it back", () => {
    const { textarea, returnFocus } = composer();
    const { card: element, control: trigger } = card("c1");
    const item = document.createElement("button");
    document.body.append(item);
    const focusReturn = createResolvedFocusReturn(returnFocus);
    item.focus();
    focusReturn.note("c1");
    focusReturn.settle();
    expect(returnFocus).not.toHaveBeenCalled();

    item.remove();
    focusReturn.settle();
    expect(document.activeElement).toBe(textarea);
    trigger.focus();
    focusReturn.settle();
    expect(returnFocus).toHaveBeenCalledOnce();

    element.setAttribute("inert", "");
    focusReturn.settle(true);
    expect(returnFocus).toHaveBeenCalledTimes(2);
    expect(document.activeElement).toBe(textarea);
  });

  it("leaves focus the person moved to another card or control", () => {
    const { returnFocus } = composer();
    const { card: resolving, control } = card("c1");
    const { control: elsewhere } = card("c2");
    const focusReturn = createResolvedFocusReturn(returnFocus);
    control.focus();
    focusReturn.note("c1");
    elsewhere.focus();
    focusReturn.settle();
    resolving.remove();
    focusReturn.settle(true);
    expect(returnFocus).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(elsewhere);
  });

  it("ignores resolutions made without focus", () => {
    const { returnFocus } = composer();
    const focusReturn = createResolvedFocusReturn(returnFocus);
    focusReturn.note("c1");
    focusReturn.settle(true);
    expect(returnFocus).not.toHaveBeenCalled();
  });
});
