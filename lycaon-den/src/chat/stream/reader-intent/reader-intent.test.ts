// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

// Tests opt into resize callbacks when needed.
vi.hoisted(() => {
  class MockResizeObserver {
    observe = vi.fn();
    unobserve = vi.fn();
    disconnect = vi.fn();
  }
  globalThis.ResizeObserver =
    MockResizeObserver as unknown as typeof ResizeObserver;
});

vi.mock("overlayscrollbars", () => ({ OverlayScrollbars: () => undefined }));

import { glideStreamToTail, setStreamTailPin, shiftStreamContent, streamScrollEdges } from "../stream-scroll.ts";
import { suppressStreamScrollEngagement } from "./reader-state.ts";

import { isStreamSpringScrolling } from "../stream-scroll-spring.ts";
import { flushScrollportFrameForTests } from "../../../platform/scrolling/scrollport-frame.ts";

import { scrollportMotionForHost } from "../../../platform/scrolling/scrollport-motion.ts";
import { beginShellLayoutBusy, endShellLayoutBusy, resetShellLayoutBusyForTests } from "../../../shell/shell-layout-busy.ts";

import { installStreamScrollCleanup, mockScrollContainer, readerFixture, key } from "../stream-scroll-test-fixture.ts";
installStreamScrollCleanup();

describe("the reader's own scrolling", () => {
  it.each(["PageDown", "Tab"])("a disclosure click supersedes deferred %s scrolling", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 300, clientHeight: 100 });
    key(input, f.el);
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.el.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    document.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    f.handlers.stopFollowing();
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(false);
    f.stop();
  });

  it.each(["keyboard", "wheel"])("a newer upward %s intent supersedes a deferred downward scroll", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 300, clientHeight: 100 });
    key("PageDown", f.el);
    f.el.scrollTop = 400;
    if (input === "keyboard") key("PageUp", f.el);
    else f.wheel(-40);
    expect(f.handlers.following()).toBe(false);
    // Native scroll events can arrive after the direction changes.
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(false);
    f.stop();
  });

  it("stops following when the reader wheels up a scrollable transcript", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.wheel(-40);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    f.stop();
  });

  it("ignores an upward wheel on a transcript that cannot scroll", () => {
    const f = readerFixture({ height: 80, scrollTop: 0, clientHeight: 100 });
    f.wheel(-40);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("never stops following on a downward wheel", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.wheel(40);
    f.scrollTo(400);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    f.stop();
  });

  it("reads upward motion without reader input as layout, not reading", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("returns a pinned tail that a layout clamp moved without reader input", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    setStreamTailPin(f.el, f.handlers.following);
    // WebKit clamps while laying out a newly inserted size container, then restores the range.
    f.scrollTo(246);
    expect(f.el.scrollTop).toBe(400);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    setStreamTailPin(f.el, null);
    f.stop();
  });

  it("leaves a layout clamp in place for a reader who is not following", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    setStreamTailPin(f.el, f.handlers.following);
    f.scrollTo(246);
    expect(f.el.scrollTop).toBe(246);
    setStreamTailPin(f.el, null);
    f.stop();
  });

  it("keeps following through a sideways swipe over wide content", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaX: 60, deltaY: -4 }));
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("reads a layout move after Tab elsewhere as layout", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const composer = document.createElement("textarea");
    document.body.append(composer);
    key("Tab", composer);
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("reads a focus reveal after Tab into the transcript as reading", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const chip = document.createElement("button");
    f.el.append(chip);
    key("Tab");
    chip.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("leaves a key a transcript control handled to that control", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const menu = document.createElement("div");
    menu.tabIndex = 0;
    f.el.append(menu);
    menu.addEventListener("keydown", (event) => event.preventDefault());
    key("ArrowUp", menu);
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("stops following for an upward scroll after a scroll key, but not one typed in a field", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    const input = document.createElement("input");
    document.body.append(input);
    key("PageUp", input);
    f.scrollTo(800);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();

    key("PageUp");
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.scrollTo(700);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it.each(["Home", "PageUp"])("%s pauses follow when new content outgrows a cached empty range", (input) => {
    const f = readerFixture({ height: 80, scrollTop: 0, clientHeight: 100 });
    streamScrollEdges(f.el);
    Object.defineProperty(f.el, "scrollHeight", { value: 500, configurable: true });
    f.el.scrollTop = 400;
    key(input, f.el);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("stops following for an upward drag and forgets the pointer on release", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.scrollTo(850);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();

    f.handlers.resumeFollowing();
    document.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    f.scrollTo(800);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("resumes following when the reader scrolls back down to the tail", () => {
    const f = readerFixture({ height: 500, scrollTop: 200, clientHeight: 100, following: false });
    f.wheel(40);
    f.scrollTo(300);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();

    f.wheel(40);
    f.scrollTo(330);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("does not resume following for an application move to the tail", () => {
    const f = readerFixture({ height: 500, scrollTop: 200, clientHeight: 100, following: false });
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("stops following when the reader drags the scrollbar thumb up", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    const motion = scrollportMotionForHost(f.el)!;
    motion.input.beginThumbGesture();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    f.scrollTo(700);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    motion.input.endThumbGesture();
    f.stop();
  });

  it("resumes following when the scrollbar thumb returns to the tail", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 500, clientHeight: 100, following: false });
    const motion = scrollportMotionForHost(f.el)!;
    motion.input.beginThumbGesture();
    f.scrollTo(890);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    motion.input.endThumbGesture();
    f.stop();
  });

  it("reads a track click's landing, observed after the click, as reading", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 500, clientHeight: 100, following: false });
    const motion = scrollportMotionForHost(f.el)!;
    motion.input.beginThumbGesture();
    motion.input.endThumbGesture();
    f.scrollTo(900);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("leaves wheel input on the scrollport frame to the wheel's own direction", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 500, clientHeight: 100, following: false });
    scrollportMotionForHost(f.el)!.input.noteNativeInput("wheel");
    f.scrollTo(900);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it.each(["wheel", "End", "PageDown", "ArrowDown"])("resumes on %s at the tail without a scroll event", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    if (input === "wheel") f.wheel(40);
    else key(input, f.el);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("resumes on downward input before the conversation has overflow", () => {
    const f = readerFixture({ height: 80, scrollTop: 0, clientHeight: 100, following: false });
    f.wheel(40);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("resumes on End before new content can move the destination away", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100, following: false });
    key("PageDown", f.el);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    key("End", f.el);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it.each([true, false])("claims End without native scrolling when following=%s", (following) => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100, following });
    const event = new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true });
    f.el.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.handlers.stopFollowing();
    f.scrollTo(900);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    expect(f.handlers.following()).toBe(false);
    f.stop();
  });

  it("leaves following alone when scrolling an expanded tool output", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const output = mockScrollContainer(600, 200, 100);
    f.el.append(output);
    output.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: -40 }));
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();

    f.handlers.stopFollowing();
    output.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: 40 }));
    key("End", output);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    expect(f.handlers.onReaderInput).not.toHaveBeenCalled();
    f.stop();
  });

  it("does not read a content shift above the reader as the reader leaving", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 600, clientHeight: 100 });
    key("PageUp");
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.handlers.stopFollowing.mockClear();
    shiftStreamContent(f.el, -200, f.el.scrollTop);
    expect(f.el.scrollTop).toBe(400);
    f.el.dispatchEvent(new Event("scroll"));
    flushScrollportFrameForTests(f.el);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("stops following when the reader selects transcript text", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const text = document.createElement("p");
    text.textContent = "Selectable transcript text";
    f.el.append(text);

    const caret = document.createRange();
    caret.setStart(text.firstChild!, 3);
    caret.collapse(true);
    document.getSelection()?.addRange(caret);
    document.dispatchEvent(new Event("selectionchange"));
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();

    document.getSelection()?.removeAllRanges();
    const range = document.createRange();
    range.selectNodeContents(text);
    document.getSelection()?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();

    // Existing selection survives the jump without stopping subsequent arrivals.
    f.handlers.resumeFollowing();
    document.dispatchEvent(new Event("selectionchange"));
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it.each(["wheel", "pointer", "keyboard"])("accepts %s input during attachment suppression", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    suppressStreamScrollEngagement(f.el, 1_000);
    if (input === "wheel") f.wheel(-40);
    if (input === "pointer") f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    if (input === "keyboard") key("PageUp");
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    if (input === "wheel") f.wheel(40);
    if (input === "keyboard") key("End");
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("does not turn attachment geometry changes into reader input", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    suppressStreamScrollEngagement(f.el, 1_000);
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.onReaderInput).not.toHaveBeenCalled();
    f.stop();
  });

  it("keeps a jump glide alive when the reader clicks a card control", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100 });
    const button = document.createElement("button");
    f.el.append(button);
    void glideStreamToTail(f.el);
    button.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    button.click();
    document.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    expect(isStreamSpringScrolling(f.el)).toBe(true);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.wheel(-40);
    expect(isStreamSpringScrolling(f.el)).toBe(false);
    f.stop();
  });

  it("cancels a jump glide on upward wheel", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100, following: false });
    void glideStreamToTail(f.el);
    expect(isStreamSpringScrolling(f.el)).toBe(true);

    f.wheel(-40);

    expect(isStreamSpringScrolling(f.el)).toBe(false);
    f.stop();
  });

  it("does not release following or treat scroll drop as user input during unstable shell layout", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    beginShellLayoutBusy();
    try {
      f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      f.scrollTo(0);
      expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
      expect(f.handlers.following()).toBe(true);
    } finally {
      endShellLayoutBusy();
      resetShellLayoutBusyForTests();
      f.stop();
    }
  });

  it("does not release following or treat scroll drop as user input when stream height collapses to 0", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 0 });
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.scrollTo(0);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(true);
    f.stop();
  });

  it("clicking interactive controls does not register reader input or release following on layout shift", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    const tab = document.createElement("div");
    tab.className = "tabs__tab";
    f.el.append(tab);
    tab.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.scrollTo(0);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(true);
    f.stop();
  });

  function pointer(type: string, clientY: number, pointerType: string): MouseEvent {
    const event = new MouseEvent(type, { bubbles: true, clientY });
    Object.defineProperty(event, "pointerType", { value: pointerType });
    return event;
  }

  it("a mouse drag-selection toward the tail never resumes following", () => {
    const f = readerFixture({ height: 500, scrollTop: 380, clientHeight: 100, following: false });
    f.el.dispatchEvent(pointer("pointerdown", 100, "mouse"));
    document.dispatchEvent(pointer("pointermove", 160, "mouse"));
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    document.dispatchEvent(pointer("pointerup", 160, "mouse"));
    f.stop();
  });

  it("a press that has made a selection never resumes following", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    const text = document.createElement("p");
    text.textContent = "Selectable transcript text";
    f.el.append(text);
    f.el.dispatchEvent(pointer("pointerdown", 100, "touch"));
    const range = document.createRange();
    range.selectNodeContents(text);
    document.getSelection()?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    document.dispatchEvent(pointer("pointermove", 160, "touch"));
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("a touch drag toward the tail still resumes following", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    f.el.dispatchEvent(pointer("pointerdown", 100, "touch"));
    document.dispatchEvent(pointer("pointermove", 160, "touch"));
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("vertical pointer drag beyond threshold registers reader input and releases following", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientY: 200 }));
    document.dispatchEvent(new MouseEvent("pointermove", { bubbles: true, clientY: 190 }));
    f.scrollTo(850);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });
});
