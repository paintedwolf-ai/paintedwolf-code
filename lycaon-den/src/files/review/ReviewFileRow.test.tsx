import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { ReviewListRow } from "./review-model.ts";
import { ReviewFileRow } from "./ReviewFileRow.tsx";
import { TEST_OWNER_PERSON_ID } from "../../platform/connection/host-identity-test.ts";

function listRow(overrides: Partial<ReviewListRow> = {}): ReviewListRow {
  return {
    fileId: "file-gone",
    rootId: "r1",
    path: "src/gone.ts",
    basename: "gone.ts",
    dirname: "src",
    op: "delete",
    lastTs: "2026-08-14T12:00:00Z",
    attribution: "",
    steps: [],
    tip: { state: "absent" },
    unpresentedAgentEffects: 0,
    presentationEffectId: null,
    presentationOrdinal: null,
    contributors: [{ kind: "agent", sessionId: "", personId: "", label: "" }],
    liveVerb: null,
    sandbox: false,
    added: null,
    removed: 12,
    ...overrides,
    headMatch: overrides.headMatch ?? "unknown",
  };
}

describe("ReviewFileRow revert", () => {
  it("renders contributors in detail without chat buttons", () => {
    const onOpen = vi.fn();
    render(() => <ReviewFileRow row={listRow({ contributors: [
      { kind: "agent", sessionId: "chat-a", personId: "", label: "Fix auth" },
      { kind: "agent", sessionId: "chat-b", personId: "", label: "Add tests" },
      { kind: "user", sessionId: "chat-a", personId: TEST_OWNER_PERSON_ID, label: "" },
    ] })} reserveStat={false} expanded stepsOpen={false} onOpen={onOpen}
      onToggleSteps={() => {}} onOpenStep={() => {}} onRowMenu={() => {}} />);
    expect(screen.getByText("Fix auth")).toBeTruthy();
    expect(screen.getByText("Add tests")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /open chat/i })).toBeNull();
  });

  it("keeps Revert visible on a tombstone", () => {
    const onRevert = vi.fn();
    render(() => (
      <ReviewFileRow
        row={listRow()}
        reserveStat={false}
        expanded={false}
        stepsOpen={false}
        onOpen={() => {}}
        onRevert={onRevert}
        onToggleSteps={() => {}}
        onOpenStep={() => {}}
        onRowMenu={() => {}}
      />
    ));
    const button = screen.getByTestId("changes-row-revert");
    expect(button.className).toContain("den-review-revert--ready");
    expect(button.getAttribute("aria-label")).toBe("Restore gone.ts to comparison baseline");
    expect(button.querySelector(".den-theme-icon")).not.toBeNull();
    expect(button.textContent).toBe("");
    fireEvent.click(button);
    expect(onRevert).toHaveBeenCalledTimes(1);
  });
});
