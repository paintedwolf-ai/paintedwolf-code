import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import type { AttentionRow } from "../../api/types.ts";
import { SplitFitRestoreButton } from "./SplitFitRestore.tsx";

function attention(cls: AttentionRow["class"], reason: AttentionRow["reason"]) {
  return { session_id: "s1", project_id: "p1", class: cls, reason } as AttentionRow;
}

const doubleChevron = (el: HTMLElement) =>
  el.querySelectorAll(".den-sidebar-automatic-expand-icon svg").length;

describe("split fit restore", () => {
  it("names the column the width took, and claims it back", () => {
    const onClick = vi.fn();
    render(() => (
      <SplitFitRestoreButton
        pane="stage"
        label="Files"
        side="left"
        onClick={onClick}
        refusals={() => 0}
      />
    ));
    const widen = screen.getByTestId("split-fit-restore-stage");
    expect(widen.getAttribute("aria-label")).toBe("Widen window to show Files");
    // The vocabulary for transiently hidden: it returns with more room.
    expect(doubleChevron(widen)).toBe(2);
    fireEvent.click(widen);
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("points at the edge the column occupies", () => {
    const mirrored = (id: string) =>
      screen.getByTestId(id).querySelector("svg")!.classList.contains(
        "den-theme-icon--mirror",
      );
    const right = render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="right"
        onClick={vi.fn()}
        refusals={() => 0}
      />
    ));
    expect(mirrored("split-fit-restore-conversation")).toBe(true);
    right.unmount();

    render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="left"
        onClick={vi.fn()}
        refusals={() => 0}
      />
    ));
    expect(mirrored("split-fit-restore-conversation")).toBe(false);
  });

  it("answers a refused claim where it was made, then says why", async () => {
    const [refusals, setRefusals] = createSignal(0);
    render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="right"
        refusals={refusals}
        onClick={vi.fn()}
      />
    ));
    const widen = screen.getByTestId("split-fit-restore-conversation");
    expect(widen.hasAttribute("data-refused")).toBe(false);

    setRefusals(1);
    // The nudge runs toward the edge the column would have returned from.
    expect(widen.getAttribute("data-refused")).toBe("right");
    // The refusal updates both the accessible label and tooltip.
    expect(widen.getAttribute("aria-label")).toBe(
      "No room to widen — the window already fills the display",
    );
    expect(widen.getAttribute("data-tip")).toBe(widen.getAttribute("aria-label"));

    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(widen.hasAttribute("data-refused")).toBe(false);
    // The refusal stands until the claim re-arms; only the motion was brief.
    expect(widen.getAttribute("aria-label")).toContain("No room to widen");

    setRefusals(0);
    expect(widen.getAttribute("aria-label")).toBe(
      "Widen window to show conversation",
    );
  });

  it("keeps a waiting conversation legible through a refusal", () => {
    const [refusals, setRefusals] = createSignal(0);
    render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="right"
        attention={attention("needs_you", "checkpoint")}
        refusals={refusals}
        onClick={vi.fn()}
      />
    ));
    const widen = screen.getByTestId("split-fit-restore-conversation");
    setRefusals(1);
    expect(widen.getAttribute("aria-label")).toBe(
      "No room to widen — the window already fills the display — Waiting on approval",
    );
    expect(widen.querySelector(".den-pane-toggle__dot")).toBeTruthy();
  });

  it("marks the two states worth interrupting for, in their own tones", () => {
    const waiting = render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="right"
        attention={attention("needs_you", "checkpoint")}
        onClick={vi.fn()}
        refusals={() => 0}
      />
    ));
    let widen = screen.getByTestId("split-fit-restore-conversation");
    expect(widen.getAttribute("aria-label")).toBe(
      "Widen window to show conversation — Waiting on approval",
    );
    expect(
      widen.querySelector(".den-pane-toggle__dot")?.getAttribute(
        "data-attention-class",
      ),
    ).toBe("needs_you");
    waiting.unmount();

    const finished = render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="right"
        attention={attention("finished", "turn_finished")}
        onClick={vi.fn()}
        refusals={() => 0}
      />
    ));
    widen = screen.getByTestId("split-fit-restore-conversation");
    expect(
      widen.querySelector(".den-pane-toggle__dot")?.getAttribute(
        "data-attention-class",
      ),
    ).toBe("finished");
    finished.unmount();

    render(() => (
      <SplitFitRestoreButton
        pane="conversation"
        label="conversation"
        side="right"
        attention={attention("running", "turn_running")}
        onClick={vi.fn()}
        refusals={() => 0}
      />
    ));
    widen = screen.getByTestId("split-fit-restore-conversation");
    expect(widen.getAttribute("aria-label")).toBe(
      "Widen window to show conversation",
    );
    expect(widen.querySelector(".den-pane-toggle__dot")).toBeNull();
  });
});
