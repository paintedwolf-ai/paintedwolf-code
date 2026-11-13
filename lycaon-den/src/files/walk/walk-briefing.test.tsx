import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { WalkPageLede, WalkSummaryLine, type WalkStepBriefing } from "./walk-briefing.tsx";

function briefing(over: Partial<WalkStepBriefing> = {}): WalkStepBriefing {
  return { sentence: undefined, evidence: undefined, available: true, failed: false, retry: vi.fn(), ...over };
}

describe("Walk briefing menus", () => {
  it("offers retry from the counts line without adding anything to the step", () => {
    const failed = briefing({ failed: true });
    render(() => <WalkSummaryLine briefing={failed}>Added 3 lines.</WalkSummaryLine>);
    fireEvent.contextMenu(screen.getByTestId("review-walk-summary"));
    screen.getByText("Retry summary").click();
    expect(failed.retry).toHaveBeenCalledOnce();
  });

  it("offers regenerate on a grouped step's lede and shows the caveat alone without a sentence", () => {
    const settled = briefing();
    render(() => <WalkPageLede caveat="Observed files may include other writes." briefing={settled} />);
    expect(screen.getByTestId("walk-page-lede").textContent).toBe("Observed files may include other writes.");
    fireEvent.contextMenu(screen.getByTestId("walk-page-lede"));
    expect(screen.getByText("Regenerate summary")).toBeTruthy();
  });

  it("has no menu for a step without a target", () => {
    render(() => <WalkSummaryLine briefing={briefing({ available: false })}>Added 3 lines.</WalkSummaryLine>);
    fireEvent.contextMenu(screen.getByTestId("review-walk-summary"));
    expect(screen.queryByText("Regenerate summary")).toBeNull();
  });
});
