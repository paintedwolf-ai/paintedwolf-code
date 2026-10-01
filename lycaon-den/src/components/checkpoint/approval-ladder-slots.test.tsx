import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import {
  approvalOptionFixture,
  toolApprovalFixture,
} from "../../chat/checkpoint/approval-test-fixtures.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";

/** Test fixture helper for ladder slot assertions. */
function tunnelCard(overrides: Partial<PendingCheckpoint> = {}): PendingCheckpoint {
  return {
    checkpointId: "ladder-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: new Date(Date.now() - 6 * 60_000).toISOString(),
    tool_approval: toolApprovalFixture({
      tool: "command",
      command: "uv sync --extra dev",
      title: "Allow network: pypi.org:443",
      presentation: {
        gate: "agent_chosen_outbound",
        lead: "First connection to this host in this chat",
        impact: "Open a TLS/CONNECT tunnel to pypi.org on port 443",
      },
      recommendedOptionId: "allow_task",
      options: [
        approvalOptionFixture({ id: "approve_current_action", rung: "once", title: "Allow once" }),
        approvalOptionFixture({
          id: "allow_day", kind: "lease", rung: "day", scope: "chat",
          title: "Allow for 1 day", coverage: "only this exact action and arguments",
          expires_when: "in 1 day or when this chat is deleted",
        }),
        approvalOptionFixture({
          id: "allow_task", kind: "lease", rung: "chat", scope: "chat",
          title: "Allow for this chat", coverage: "only this exact action and arguments",
          expires_when: "when this chat is deleted",
        }),
        approvalOptionFixture({
          id: "allow_project", kind: "lease", rung: "project", scope: "project",
          title: "Allow for this project for 7 days",
          coverage: "connections to `pypi.org` and its subdomains on port 443",
          expires_when: "in 7 days or when revoked",
          disabled: true, note: "Not available: no project is open to attach a longer approval to",
        }),
        approvalOptionFixture({
          id: "quiet_chat", kind: "quiet", rung: "chat", group: "Allow and stop asking",
          title: "For this chat", coverage: "pypi.org",
          expires_when: "when this chat is deleted or you revoke it",
        }),
      ],
    }),
    ...overrides,
  };
}

function mount(checkpoint: PendingCheckpoint, onToolApproval = vi.fn()) {
  const view = render(() => (
    <ApprovalCard
      checkpoint={checkpoint}
      resolving={false}
      onToolApproval={onToolApproval}
      onContentApply={() => {}}
    />
  ));
  return { view, onToolApproval };
}

describe("approval ladder slots", () => {
  it("leads with the distinguishing fact and shows the face's coverage and expiry", () => {
    const { view } = mount(tunnelCard());
    expect(view.getByTestId("approval-lead").textContent).toBe(
      "First connection to this host in this chat",
    );
    expect(view.getByTestId("approval-face-meta").textContent).toBe(
      "only this exact action and arguments · when this chat is deleted",
    );
    expect(view.getByTestId("approval-wait").textContent).toBe("Waiting 6 min");
  });

  it("keeps an unreachable rung in its slot, disabled, with the host note", () => {
    const { view } = mount(tunnelCard());
    fireEvent.click(view.getByTestId("approval-grant-face"));
    const menu = screen.getByTestId("approval-grant-menu");
    const rows = [...menu.querySelectorAll<HTMLButtonElement>("[role=menuitem]")];
    // Each row shows the digit that picks it: the face (chat, slot 3) is not
    // in the menu, and the quiet choice has no digit at all.
    expect(rows.map((el) => el.getAttribute("data-slot"))).toEqual(["1", "2", "4", null]);
    expect(rows[3]?.querySelector("kbd")).toBeNull();
    expect(rows[3]?.getAttribute("aria-label")).toBe("For this chat");
    const project = rows.find((el) => el.dataset.offerId === "allow_project");
    expect(project?.disabled).toBe(true);
    expect(project?.getAttribute("aria-disabled")).toBe("true");
    expect(project?.querySelector("[data-testid=approval-grant-item-note]")?.textContent).toBe(
      "Not available: no project is open to attach a longer approval to",
    );
    // The quiet slot keeps its position after the disabled rung.
    expect(rows[3]?.dataset.offerId).toBe("quiet_chat");
  });

  it("maps digit keys to fixed semantic rungs and ignores a disabled slot", () => {
    const { view, onToolApproval } = mount(tunnelCard());
    const card = view.getByTestId("tool-approval-card");
    card.focus();
    fireEvent.keyDown(card, { key: "1" });
    expect(onToolApproval).toHaveBeenCalledWith("approve", { optionId: "approve_current_action" });
    onToolApproval.mockClear();
    fireEvent.keyDown(card, { key: "2" });
    expect(onToolApproval).toHaveBeenCalledWith("approve", { optionId: "allow_day" });
    onToolApproval.mockClear();
    fireEvent.keyDown(card, { key: "3" });
    expect(onToolApproval).toHaveBeenCalledWith("approve", { optionId: "allow_task" });
    onToolApproval.mockClear();
    fireEvent.keyDown(card, { key: "4" });
    expect(onToolApproval).not.toHaveBeenCalled();
    // Digits past the ladder do nothing, and a grouped choice never answers a digit.
    fireEvent.keyDown(card, { key: "5" });
    expect(onToolApproval).not.toHaveBeenCalled();
  });

  it("never lets a digit fall through to a grouped option", () => {
    const base = tunnelCard();
    const plan = base.tool_approval!.plan;
    const checkpoint: PendingCheckpoint = {
      ...base,
      tool_approval: {
        ...base.tool_approval!,
        plan: {
          ...plan,
          options: [
            ...plan.options,
            approvalOptionFixture({
              id: "trust_provider", kind: "lease", rung: "device", scope: "device", group: "Trust",
              title: "Trust this provider with credentials", coverage: "this destination",
              expires_when: "when you untrust the provider in Settings",
            }),
          ],
        },
      },
    };
    const { view, onToolApproval } = mount(checkpoint);
    const card = view.getByTestId("tool-approval-card");
    card.focus();
    fireEvent.keyDown(card, { key: "4" });
    expect(onToolApproval).not.toHaveBeenCalled();
  });
  it("answers slot 4 with a device rung when the subject is machine-level", () => {
    const base = tunnelCard();
    const plan = base.tool_approval!.plan;
    const checkpoint: PendingCheckpoint = {
      ...base,
      tool_approval: {
        ...base.tool_approval!,
        plan: {
          ...plan,
          options: plan.options.map((option) =>
            option.id === "allow_project"
              ? { ...option, id: "allow_device", rung: "device", scope: "device", disabled: false, note: undefined }
              : option),
        },
      },
    };
    const { view, onToolApproval } = mount(checkpoint);
    const card = view.getByTestId("tool-approval-card");
    card.focus();
    fireEvent.keyDown(card, { key: "4" });
    expect(onToolApproval).toHaveBeenCalledWith("approve", { optionId: "allow_device" });
  });
});

describe("high-risk cards signal, but take the same single action", () => {
  function highRiskCard(): PendingCheckpoint {
    const base = tunnelCard();
    return {
      ...base,
      tool_approval: {
        ...base.tool_approval!,
        consequence_band: "high_risk",
        consequence_code: "secret",
        plan: {
          ...base.tool_approval!.plan,
          subject: {
            kind: "secret",
            title: "Credential detected before sending this model request",
            targets: [{ kind: "secret", label: "AWS access key ID", details: { generic_shape: "a1b2 (4 characters)" } }],
          },
          presentation: {
            ...base.tool_approval!.plan.presentation,
            location: {
              origin: ".env:3",
              destination: "Fireworks 1",
              origin_kind: "file",
              destination_kind: "model_provider",
            },
          },
        },
      },
    };
  }

  it("labels the risk but keeps the standard single-action controls", () => {
    const { view, onToolApproval } = mount(highRiskCard());
    // The band and its consequence line are the signal.
    expect(view.getByTestId("approval-high-risk-label")).toBeTruthy();
    // No acknowledgement step: the face is enabled and Enter-approvable like any card.
    expect(view.queryByTestId("approval-high-risk-ack")).toBeNull();
    const primary = view.getByTestId("approval-approve-primary") as HTMLButtonElement;
    expect(primary.disabled).toBe(false);
    expect(primary.querySelector("kbd")).not.toBeNull();
    const card = view.getByTestId("tool-approval-card");
    card.focus();
    fireEvent.keyDown(card, { key: "Enter" });
    expect(onToolApproval).toHaveBeenCalledWith("approve", { optionId: "allow_task" });
  });
});
