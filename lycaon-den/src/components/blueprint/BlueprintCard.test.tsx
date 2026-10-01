import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import type { BlueprintMeta } from "../../api/types.ts";
import { blueprintCardViewFromMeta } from "../../blueprint/blueprint-card-view.ts";
import { BlueprintCard } from "./BlueprintCard.tsx";

function meta(overrides: Partial<BlueprintMeta> = {}): BlueprintMeta {
  return {
    blueprint_path: "blueprint-1",
    revision: 1,
    revision_key: "rev-1",
    status: "awaiting_approval",
    phase: "ready",
    phase_label: "Ready for review",
    blueprint_title: "Scroll model rework",
    can_approve: true,
    collapsed: false,
    show_actions: true,
    ...overrides,
  };
}

function renderCard(
  planMeta: BlueprintMeta = meta(),
  content: string = "## Goal\n\nShip it",
) {
  const handlers = {
    onRevise: vi.fn(),
    onOpen: vi.fn(),
    onApprove: vi.fn(),
    onReject: vi.fn(),
    onChoiceTransition: vi.fn(),
  };
  render(() => (
    <BlueprintCard
      anchorId="m-blueprint"
      view={blueprintCardViewFromMeta(planMeta)}
      blueprintName={planMeta.blueprint_title}
      content={content}
      loading={false}
      warning={null}
      choiceTransitions={[
        { id: "critique", label: "Run critique", armed: true },
        { id: "side_quest", label: "Side quest", armed: false },
      ]}
      {...handlers}
    />
  ));
  return handlers;
}

describe("BlueprintCard", () => {
  it("asks for the decision while the host is waiting on one", () => {
    const handlers = renderCard();
    expect(screen.getByTestId("blueprint-card-phase").textContent).toBe(
      "Ready for review",
    );
    screen.getByTestId("blueprint-approve").click();
    screen.getByTestId("blueprint-reject").click();
    screen.getByTestId("blueprint-open").click();
    expect(handlers.onApprove).toHaveBeenCalledOnce();
    expect(handlers.onReject).toHaveBeenCalledOnce();
    expect(handlers.onOpen).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("blueprint-card-decision")).toBeNull();
  });

  // Arms come from the manifest, so every one of them renders.
  it("renders each host choice arm and respects its armed flag", () => {
    const handlers = renderCard();
    const side = screen.getByTestId("blueprint-choice-side_quest") as HTMLButtonElement;
    expect(side.disabled).toBe(true);
    screen.getByTestId("blueprint-choice-critique").click();
    expect(handlers.onChoiceTransition).toHaveBeenCalledWith("critique");
  });

  it("disables approve when the host is not ready for it", () => {
    renderCard(meta({ can_approve: false }));
    expect(
      (screen.getByTestId("blueprint-approve") as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("opens the revise form in place of the decision", () => {
    const handlers = renderCard();
    screen.getByTestId("blueprint-revise").click();
    const input = screen.getByTestId("blueprint-revise-input") as HTMLTextAreaElement;
    input.value = "tighten the scope";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    screen.getByTestId("blueprint-revise-send").click();
    expect(handlers.onRevise).toHaveBeenCalledWith("tighten the scope");
  });

  it("stays as the record after approval", () => {
    renderCard(
      meta({
        status: "approved",
        phase: "building",
        phase_label: "Building…",
        can_approve: false,
        collapsed: true,
        show_actions: false,
      }),
    );
    const decision = screen.getByTestId("blueprint-card-decision");
    expect(decision.getAttribute("data-decision")).toBe("approved");
    expect(decision.textContent).toContain("Approved");
    expect(decision.textContent).toContain("Scroll model rework");
    expect(screen.queryByTestId("blueprint-approve")).toBeNull();
    expect(screen.queryByTestId("blueprint-reject")).toBeNull();
    // The blueprint stays reachable from its own record.
    expect(screen.getByTestId("blueprint-open")).toBeTruthy();
    // The completed phase belongs on the same baseline as the remaining action.
    const actionRow = screen.getByTestId("blueprint-card-actions");
    expect(actionRow.contains(screen.getByTestId("blueprint-card-phase"))).toBe(true);
    expect(actionRow.contains(screen.getByTestId("blueprint-open"))).toBe(true);
  });

  it("records a rejection, and the host's word for a superseded run", () => {
    const rejected = meta({
      status: "rejected",
      phase: "rejected",
      phase_label: "Rejected",
      can_approve: false,
      collapsed: true,
      show_actions: false,
    });
    const { unmount } = render(() => (
      <BlueprintCard
        anchorId="m-blueprint"
        view={blueprintCardViewFromMeta(rejected)}
        blueprintName={rejected.blueprint_title}
        content=""
        loading={false}
        warning={null}
        onRevise={vi.fn()}
        onOpen={vi.fn()}
      />
    ));
    let decision = screen.getByTestId("blueprint-card-decision");
    expect(decision.getAttribute("data-decision")).toBe("rejected");
    expect(decision.textContent).toContain("Rejected");
    unmount();

    renderCard(
      meta({
        status: "superseded",
        phase: "rejected",
        phase_label: "Superseded",
        can_approve: false,
        collapsed: true,
        show_actions: false,
      }),
    );
    decision = screen.getByTestId("blueprint-card-decision");
    expect(decision.getAttribute("data-decision")).toBe("rejected");
    expect(decision.textContent).toContain("Superseded");
  });

  // A decided row is a record you can still read, not a dead end.
  it("lets the reader open a collapsed record back up", () => {
    renderCard(
      meta({
        status: "approved",
        phase: "building",
        phase_label: "Building…",
        can_approve: false,
        collapsed: true,
        show_actions: false,
      }),
    );
    expect(screen.queryByTestId("blueprint-card-body")).toBeNull();
    screen.getByTestId("blueprint-card-toggle").click();
    expect(screen.getByTestId("blueprint-card-body")).toBeTruthy();
  });

  // A stub carries nothing beyond the title row, so there is nothing to toggle.
  it("offers no toggle or body for a title-only stub", () => {
    renderCard(meta(), "---\ntitle: Scroll model rework\n---\n\n# Scroll model rework\n");
    expect(screen.getByTestId("blueprint-card").textContent).toContain(
      "Scroll model rework",
    );
    expect(screen.queryByTestId("blueprint-card-toggle")).toBeNull();
    expect(screen.queryByTestId("blueprint-card-body")).toBeNull();
    expect(screen.getByTestId("blueprint-approve")).toBeTruthy();
  });
});
