// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  clearRevealHighlight,
  paintRevealHighlight,
  resetRevealHighlightForTests,
  revealHighlightActive,
} from "./reveal-highlight.ts";
import { matchInRoot } from "../../../find/find-match.ts";

function row(text: string): HTMLElement {
  const el = document.createElement("div");
  el.innerHTML = `<p>${text}</p>`;
  document.body.appendChild(el);
  return el;
}

function paint(el: HTMLElement, query: string): HTMLElement | null {
  return paintRevealHighlight(el, matchInRoot(el, query, false));
}

function markCount(el: HTMLElement): number {
  return el.querySelectorAll("[data-den-find-mark]").length;
}

afterEach(() => {
  document.body.innerHTML = "";
  resetRevealHighlightForTests();
});

describe("reveal highlight lifecycle", () => {
  it("paints marks and reports itself active", () => {
    const el = row("the retry budget is exhausted");
    expect(paint(el, "retry budget")).not.toBeNull();
    expect(markCount(el)).toBeGreaterThan(0);
    expect(revealHighlightActive()).toBe(true);
  });

  it("clears the marks it painted", () => {
    const el = row("the retry budget is exhausted");
    paint(el, "retry budget");

    expect(clearRevealHighlight()).toBe(true);
    expect(markCount(el)).toBe(0);
    expect(revealHighlightActive()).toBe(false);
  });

  it("reports nothing to clear when no reveal painted", () => {
    expect(clearRevealHighlight()).toBe(false);
    expect(revealHighlightActive()).toBe(false);
  });

  it("a second reveal takes over from the first", () => {
    const first = row("the retry budget is exhausted");
    const second = row("the retry budget is restored");
    paint(first, "retry budget");
    paint(second, "retry budget");

    // Only one reveal is marked at a time.
    expect(markCount(first)).toBe(0);
    expect(markCount(second)).toBeGreaterThan(0);

    clearRevealHighlight();
    expect(markCount(second)).toBe(0);
  });

  it("goes inactive when its row unmounts, so Escape falls through", () => {
    const el = row("the retry budget is exhausted");
    paint(el, "retry budget");
    el.remove();

    expect(revealHighlightActive()).toBe(false);
    expect(clearRevealHighlight()).toBe(false);
  });
});
