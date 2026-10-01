import { DEFAULT_TRANSCRIPT_SPACING } from "./transcript-spacing.ts";
import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { SessionTranscript } from "../../../components/transcript/transcript-viewport-test-harness.tsx";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_PAGE_BUDGET,
  materializeTranscriptWindow,
} from "./transcript-window.ts";
import {
  TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS,
  TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS,
} from "./transcript-virtualizer.ts";
import { sparseWindowForSessionLength } from "./transcript-scale-fixture.ts";

const TRANSCRIPT_RESIDENT_MESSAGE_BUDGET =
  (TRANSCRIPT_PAGE_BUDGET + 1) * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
const SESSION_LENGTH = 5000;
const DOUBLE_SESSION_LENGTH = 10_000;

function uniformWindowRowBound(viewportHeight: number, rowHeight: number) {
  const rowExtent = rowHeight + DEFAULT_TRANSCRIPT_SPACING.rowGap;
  const visibleRows = Math.ceil(viewportHeight / rowExtent) + 1;
  const bufferRows = Math.max(
    TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS,
    Math.ceil(
      (viewportHeight * TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS) / rowExtent,
    ),
  );
  return visibleRows + 2 * bufferRows;
}

describe("transcript scale bounds", () => {
  it("keeps resident messages within the page-budget bound at N≥5000", () => {
    const a = sparseWindowForSessionLength(SESSION_LENGTH);
    const b = sparseWindowForSessionLength(DOUBLE_SESSION_LENGTH);
    const countA = materializeTranscriptWindow(a).length;
    const countB = materializeTranscriptWindow(b).length;
    expect(countA).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_MESSAGE_BUDGET);
    expect(countB).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_MESSAGE_BUDGET);
    // Resident rows stay bounded as session length grows.
    expect(countB).toBeLessThanOrEqual(countA * 1.05 + 1);
    expect(countA).toBeLessThan(SESSION_LENGTH / 2);
    expect(countB).toBeLessThan(DOUBLE_SESSION_LENGTH / 2);
  });

  it("bounds mounted DOM rows to the viewport runway for a max-resident materialization", () => {
    const tw = sparseWindowForSessionLength(SESSION_LENGTH);
    const messages = materializeTranscriptWindow(tw);
    expect(messages.length).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_MESSAGE_BUDGET);
    expect(messages.length).toBeGreaterThan(DEFAULT_TRANSCRIPT_PAGE_LIMIT);

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="scale-bounds" messages={messages} />
    ));
    const mounted = container.querySelectorAll(".transcript-viewport-row").length;
    const max = uniformWindowRowBound(900, 72);
    expect(mounted).toBeGreaterThan(0);
    expect(mounted).toBeLessThanOrEqual(max);
    expect(mounted).toBeLessThan(messages.length);
    expect(container.querySelector("[data-deferred]")).toBeNull();
  });
});
