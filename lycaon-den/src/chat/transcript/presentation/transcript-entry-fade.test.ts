// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  onTranscriptEnterFadeDone,
  TRANSCRIPT_ENTER_FADE_ANIMATION,
  TRANSCRIPT_ENTER_FADE_MS,
} from "./transcript-entry-fade.ts";

afterEach(() => {
  vi.useRealTimers();
});

describe("transcript entry fade", () => {
  it("finishes only for the row's own enter animation", () => {
    vi.useFakeTimers();
    const node = document.createElement("div");
    const child = document.createElement("div");
    node.append(child);
    const onDone = vi.fn();
    onTranscriptEnterFadeDone(node, onDone);

    const childEnd = new Event("animationend", { bubbles: true }) as AnimationEvent;
    Object.defineProperty(childEnd, "animationName", {
      value: TRANSCRIPT_ENTER_FADE_ANIMATION,
    });
    child.dispatchEvent(childEnd);
    expect(onDone).not.toHaveBeenCalled();

    const otherEnd = new Event("animationend") as AnimationEvent;
    Object.defineProperty(otherEnd, "animationName", { value: "other" });
    node.dispatchEvent(otherEnd);
    expect(onDone).not.toHaveBeenCalled();

    const matchingEnd = new Event("animationend") as AnimationEvent;
    Object.defineProperty(matchingEnd, "animationName", {
      value: TRANSCRIPT_ENTER_FADE_ANIMATION,
    });
    node.dispatchEvent(matchingEnd);
    expect(onDone).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("uses a bounded safety timeout when animationend is unavailable", () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    onTranscriptEnterFadeDone(document.createElement("div"), onDone);

    vi.advanceTimersByTime(TRANSCRIPT_ENTER_FADE_MS + 79);
    expect(onDone).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("cancels both the listener and safety timeout", () => {
    vi.useFakeTimers();
    const node = document.createElement("div");
    const onDone = vi.fn();
    const cancel = onTranscriptEnterFadeDone(node, onDone);

    cancel();
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(TRANSCRIPT_ENTER_FADE_MS * 2);
    const end = new Event("animationend") as AnimationEvent;
    Object.defineProperty(end, "animationName", {
      value: TRANSCRIPT_ENTER_FADE_ANIMATION,
    });
    node.dispatchEvent(end);
    expect(onDone).not.toHaveBeenCalled();
  });
});
