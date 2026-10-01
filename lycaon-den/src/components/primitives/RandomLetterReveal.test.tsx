import { render } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearLetterRevealOnceKeys,
  hasLetterRevealOnceKey,
  markLetterRevealOnceKey,
  RandomLetterReveal,
} from "./RandomLetterReveal.tsx";

describe("RandomLetterReveal", () => {
  beforeEach(() => {
    clearLetterRevealOnceKeys();
    vi.stubGlobal(
      "matchMedia",
      vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn() }),
    );
  });

  afterEach(() => {
    clearLetterRevealOnceKeys();
    vi.unstubAllGlobals();
  });

  it("marks onceKey as soon as the reveal starts, not when it finishes", async () => {
    // Remount before the first animation completes.
    vi.useFakeTimers();
    render(() => (
      <RandomLetterReveal text="ab" onceKey="chk-1" data-testid="reveal" />
    ));
    await Promise.resolve();
    expect(hasLetterRevealOnceKey("chk-1")).toBe(true);
    vi.useRealTimers();
  });

  it("shows the full text immediately on a mid-animation remount", async () => {
    vi.useFakeTimers();
    const first = render(() => (
      <RandomLetterReveal text="hello" onceKey="chk-2" data-testid="reveal" />
    ));
    await Promise.resolve();
    // Stop before the full reveal.
    await vi.advanceTimersByTimeAsync(12);
    first.unmount();

    const second = render(() => (
      <RandomLetterReveal text="hello" onceKey="chk-2" data-testid="reveal" />
    ));
    await Promise.resolve();
    const visible = second
      .getByTestId("reveal")
      .querySelectorAll(".den-letter-reveal-char--visible");
    expect(visible.length).toBe(5);
    vi.useRealTimers();
  });

  it("shows all letters immediately on remount when onceKey already revealed", async () => {
    markLetterRevealOnceKey("chk-1");
    const { getByTestId } = render(() => (
      <RandomLetterReveal text="hello" onceKey="chk-1" data-testid="reveal" />
    ));
    await Promise.resolve();
    const root = getByTestId("reveal");
    const visible = root.querySelectorAll(".den-letter-reveal-char--visible");
    expect(visible.length).toBe(5);
  });
});
