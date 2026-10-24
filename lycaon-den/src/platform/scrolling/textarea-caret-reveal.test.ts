// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { measureTextareaCaretLine, revealTextareaCaret } from "./textarea-caret-reveal.ts";
import { scrollportMotionForViewport, unbindScrollportMotion } from "./scrollport-motion.ts";
import { bindScrollportMotion } from "./scrollport-motion-test-harness.ts";

const LINE = 24;
const PADDING = 8;

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const host of hosts.splice(0)) {
    unbindScrollportMotion(host);
    host.remove();
  }
  vi.restoreAllMocks();
});

/** jsdom has no layout: the marker sits one line below each newline before it. */
function stubMirrorLayout(): void {
  vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
    const before = this.previousSibling?.textContent ?? "";
    return PADDING + before.split("\n").length * LINE - LINE;
  });
}

function rect(top: number, height: number): DOMRect {
  return { top, bottom: top + height, height, left: 0, right: 100, width: 100, x: 0, y: top, toJSON: () => ({}) };
}

/** A capped viewport scrolled to `scrollTop` over a textarea holding `value`. */
function composerFixture(value: string, opts: { viewportHeight: number; scrollTop: number }) {
  const host = document.createElement("div");
  const viewport = document.createElement("div");
  const textarea = document.createElement("textarea");
  Object.assign(textarea.style, {
    lineHeight: `${LINE}px`,
    paddingTop: `${PADDING}px`,
    paddingBottom: `${PADDING}px`,
    borderTopWidth: "0px",
  });
  textarea.value = value;
  viewport.appendChild(textarea);
  host.appendChild(viewport);
  document.body.appendChild(host);
  hosts.push(host);
  const lines = value.split("\n").length;
  const fieldHeight = lines * LINE + 2 * PADDING;
  viewport.scrollTop = opts.scrollTop;
  Object.defineProperty(viewport, "clientHeight", { configurable: true, value: opts.viewportHeight });
  Object.defineProperty(viewport, "scrollHeight", { configurable: true, value: fieldHeight });
  viewport.getBoundingClientRect = () => rect(100, opts.viewportHeight);
  textarea.getBoundingClientRect = () => rect(100 - viewport.scrollTop, fieldHeight);
  return { host, viewport, textarea };
}

describe("measureTextareaCaretLine", () => {
  it("measures the line holding the caret through a removed mirror", () => {
    stubMirrorLayout();
    const { textarea } = composerFixture("one\ntwo\nthree", { viewportHeight: 200, scrollTop: 0 });
    textarea.setSelectionRange(5, 5);
    const before = document.body.childElementCount;

    expect(measureTextareaCaretLine(textarea)).toEqual({ top: PADDING + LINE, bottom: PADDING + 2 * LINE });
    expect(document.body.childElementCount).toBe(before);
  });

  it("follows the moving end of a backward selection", () => {
    stubMirrorLayout();
    const { textarea } = composerFixture("one\ntwo\nthree", { viewportHeight: 200, scrollTop: 0 });
    textarea.setSelectionRange(1, 10, "backward");

    expect(measureTextareaCaretLine(textarea).top).toBe(PADDING);
  });

  it("places a caret after a trailing newline on the new line", () => {
    stubMirrorLayout();
    const { textarea } = composerFixture("one\n", { viewportHeight: 200, scrollTop: 0 });
    textarea.setSelectionRange(4, 4);

    expect(measureTextareaCaretLine(textarea).top).toBe(PADDING + LINE);
  });
});

describe("revealTextareaCaret", () => {
  const draft = Array.from({ length: 20 }, (_, index) => `line ${index}`).join("\n");

  it("leaves a viewport that does not overflow alone", () => {
    stubMirrorLayout();
    const { textarea, viewport } = composerFixture("short", { viewportHeight: 200, scrollTop: 0 });
    Object.defineProperty(viewport, "scrollHeight", { configurable: true, value: 200 });
    textarea.setSelectionRange(5, 5);

    revealTextareaCaret(textarea, viewport);

    expect(viewport.scrollTop).toBe(0);
  });

  it("scrolls down to the caret line and the field's bottom padding", () => {
    stubMirrorLayout();
    const { textarea, viewport } = composerFixture(draft, { viewportHeight: 200, scrollTop: 0 });
    textarea.setSelectionRange(draft.length, draft.length);

    revealTextareaCaret(textarea, viewport);

    expect(viewport.scrollTop).toBe(20 * LINE + 2 * PADDING - 200);
  });

  it("scrolls up to a caret above the viewport", () => {
    stubMirrorLayout();
    const { textarea, viewport } = composerFixture(draft, { viewportHeight: 200, scrollTop: 300 });
    const third = draft.indexOf("line 2");
    textarea.setSelectionRange(third, third);

    revealTextareaCaret(textarea, viewport);

    expect(viewport.scrollTop).toBe(2 * LINE);
  });

  it("keeps a visible caret where it is", () => {
    stubMirrorLayout();
    const { textarea, viewport } = composerFixture(draft, { viewportHeight: 200, scrollTop: 100 });
    const visible = draft.indexOf("line 6");
    textarea.setSelectionRange(visible, visible);

    revealTextareaCaret(textarea, viewport);

    expect(viewport.scrollTop).toBe(100);
  });

  it("writes through the scrollport's motion when one is bound", () => {
    stubMirrorLayout();
    const { host, textarea, viewport } = composerFixture(draft, { viewportHeight: 200, scrollTop: 0 });
    bindScrollportMotion(host, viewport, textarea);
    const motion = scrollportMotionForViewport(viewport)!;
    const revealSpan = vi.spyOn(motion, "revealSpan");
    textarea.setSelectionRange(draft.length, draft.length);

    revealTextareaCaret(textarea, viewport);

    expect(revealSpan).toHaveBeenCalledWith(100 + 19 * LINE, 100 + 20 * LINE + 2 * PADDING);
  });
});
