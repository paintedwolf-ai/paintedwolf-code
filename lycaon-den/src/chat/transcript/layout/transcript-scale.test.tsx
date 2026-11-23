import { DEFAULT_TRANSCRIPT_SPACING } from "./transcript-spacing.ts";
import { describe, expect, it } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { SessionTranscript } from "../../../components/transcript/transcript-viewport-test-harness.tsx";
import { transcriptRowOrdSpan } from "../history/transcript-runway.ts";
import { messagesToDisplayTranscriptItems } from "../projection/transcript-items.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_RESIDENT_ORD_BUDGET,
  materializeTranscriptWindow,
} from "./transcript-window.ts";
import {
  TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS,
  TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS,
  transcriptRowEstimatedHeight,
} from "./transcript-virtualizer.ts";
import { heavyActivityGroupMessages, sparseWindowForSessionLength } from "./transcript-scale-fixture.ts";

const TRANSCRIPT_RESIDENT_MESSAGE_BUDGET =
  TRANSCRIPT_RESIDENT_ORD_BUDGET + DEFAULT_TRANSCRIPT_PAGE_LIMIT;
const SESSION_LENGTH = 5000;
const DOUBLE_SESSION_LENGTH = 10_000;

function uniformWindowRowBound(viewportHeight: number, rowHeight: number) {
  const rowExtent = rowHeight + DEFAULT_TRANSCRIPT_SPACING.itemGap;
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

  it("bounds expanded 300-command 45-file activity groups, preserves proportional ord span, and avoids collapsed estimate misses", () => {
    const heavy = heavyActivityGroupMessages(300, 45);
    const items = messagesToDisplayTranscriptItems(heavy, { layout: "chat" });
    const group = items.find((item) => item.kind === "activity_group");
    expect(group?.kind).toBe("activity_group");
    expect(transcriptRowOrdSpan(group!)).toBeGreaterThanOrEqual(300);

    // Collapsed 1-, 2-, and 3-section groups match the 30px row + 1px seam + 10px card/gap geometry within <24px.
    expect(transcriptRowEstimatedHeight({ kind: "activity_group", sectionOrder: ["activities"] })).toBe(40);
    expect(transcriptRowEstimatedHeight({ kind: "activity_group", sectionOrder: ["activities", "files"] })).toBe(71);
    expect(transcriptRowEstimatedHeight({ kind: "activity_group", sectionOrder: ["activities", "approvals", "files"] })).toBe(102);

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="scale-heavy-group" messages={heavy} />
    ));
    const card = container.querySelector<HTMLElement>('[data-testid="activity-group-card"]');
    expect(card).toBeTruthy();

    fireEvent.click(card!.querySelector<HTMLButtonElement>('[data-testid="activity-span-card"] button')!);
    fireEvent.click(card!.querySelector<HTMLButtonElement>('[data-testid="diff-group"] button.den-transcript-disclosure-toggle')!);

    const mountedActivities = card!.querySelectorAll(".den-activity-span-item").length;
    const mountedDiffs = card!.querySelectorAll('[data-testid="file-edit-diff"]').length;
    expect(mountedActivities).toBeGreaterThan(0);
    expect(mountedActivities).toBeLessThan(300);
    expect(mountedDiffs).toBeGreaterThan(0);
    expect(mountedDiffs).toBeLessThan(45);
  });
});
