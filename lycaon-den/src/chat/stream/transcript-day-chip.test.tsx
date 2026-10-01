import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import {
  TranscriptViewportProvider,
  type TranscriptViewportController,
} from "./transcript-viewport.tsx";
import { TranscriptDayChip } from "./transcript-day-chip.tsx";

function controller(opts: {
  following: () => boolean;
  readingDay: () => number | null;
  stream: HTMLElement;
}): TranscriptViewportController {
  return {
    following: opts.following,
    readingDay: opts.readingDay,
    stream: () => opts.stream,
  } as unknown as TranscriptViewportController;
}

describe("TranscriptDayChip", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("names the day at the top once its label scrolls away, and hides when it returns", async () => {
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      cb(0);
      return 1;
    });
    const stream = document.createElement("div");
    const inner = document.createElement("section");
    inner.className = "den-chat-stream-inner";
    stream.append(inner);
    let day: number | null = Date.parse("2026-09-10T20:00:00Z");
    const { getByTestId } = render(() => (
      <TranscriptViewportProvider
        value={controller({ following: () => true, readingDay: () => day, stream })}
      >
        <TranscriptDayChip />
      </TranscriptViewportProvider>
    ));
    const chip = getByTestId("transcript-day-chip");
    expect(chip.classList.contains("den-transcript-day-chip--visible")).toBe(true);
    expect(chip.textContent).not.toBe("");

    day = null;
    stream.dispatchEvent(new Event("scroll"));
    await Promise.resolve();
    expect(chip.classList.contains("den-transcript-day-chip--visible")).toBe(false);
  });
});
