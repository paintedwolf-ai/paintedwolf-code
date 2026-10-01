import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { ApprovalsOffPanel } from "./ApprovalsOffPanel.tsx";
import { NEVER_ASK_COPY } from "../../../settings/security/approvals-settings-copy.ts";
import type { ApprovalConfigResponse } from "../../../api/types.ts";

const approvals = (
  overrides: Partial<ApprovalConfigResponse> = {},
): ApprovalConfigResponse => ({
  scope: "global",
  rules: [],
  managed_rules: [],
  merged_from: [],
  approval_posture: "balanced",
  ai_rationale_enabled: true,
  never_ask: false,
  ...overrides,
});

const clientWith = (initial: boolean) => ({
  getApprovalsSettings: vi.fn().mockResolvedValue(approvals({ never_ask: initial })),
  updateApprovalsSettings: vi
    .fn()
    .mockImplementation((req: { never_ask?: boolean }) =>
      Promise.resolve(approvals({ never_ask: req.never_ask ?? false })),
    ),
});

describe("ApprovalsOffPanel", () => {
  it("does not disable approvals until the acknowledgement is ticked", async () => {
    const client = clientWith(false);
    const { getByTestId, queryByTestId } = render(() => (
      <ApprovalsOffPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("approvals-off-start")).toBeTruthy());
    fireEvent.click(getByTestId("approvals-off-start"));

    // The consequence is on screen and the confirm is unreachable until acknowledged.
    expect(getByTestId("approvals-off-confirm").textContent).toContain(
      NEVER_ASK_COPY.dangerTitle,
    );
    const confirm = getByTestId("approvals-off-confirm-button") as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.click(confirm);
    expect(client.updateApprovalsSettings).not.toHaveBeenCalled();

    // DenCheckbox spreads data-testid onto the <input> itself.
    fireEvent.click(getByTestId("approvals-off-acknowledge"));
    await waitFor(() =>
      expect(
        (getByTestId("approvals-off-confirm-button") as HTMLButtonElement).disabled,
      ).toBe(false),
    );

    fireEvent.click(getByTestId("approvals-off-confirm-button"));
    await waitFor(() =>
      expect(client.updateApprovalsSettings).toHaveBeenCalledWith({ never_ask: true }),
    );
    await waitFor(() => expect(getByTestId("approvals-off-active")).toBeTruthy());
    expect(queryByTestId("approvals-off-confirm")).toBeNull();
  });

  it("cancelling clears the acknowledgement so it must be ticked again", async () => {
    const client = clientWith(false);
    const { getByTestId } = render(() => <ApprovalsOffPanel client={client as never} />);

    await waitFor(() => expect(getByTestId("approvals-off-start")).toBeTruthy());
    fireEvent.click(getByTestId("approvals-off-start"));
    fireEvent.click(getByTestId("approvals-off-acknowledge"));
    fireEvent.click(getByTestId("approvals-off-cancel"));

    fireEvent.click(getByTestId("approvals-off-start"));
    expect(
      (getByTestId("approvals-off-confirm-button") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(client.updateApprovalsSettings).not.toHaveBeenCalled();
  });

  it("shows the standing warning when already off, and restores in one click", async () => {
    const client = clientWith(true);
    const { getByTestId, queryByTestId } = render(() => (
      <ApprovalsOffPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("approvals-off-active")).toBeTruthy());
    expect(getByTestId("approvals-off-active").textContent).toContain(
      NEVER_ASK_COPY.enabledNotice,
    );
    expect(queryByTestId("approvals-off-start")).toBeNull();

    fireEvent.click(getByTestId("approvals-off-restore"));
    await waitFor(() =>
      expect(client.updateApprovalsSettings).toHaveBeenCalledWith({ never_ask: false }),
    );
    await waitFor(() => expect(getByTestId("approvals-off-start")).toBeTruthy());
  });

  // A GET failure must not render "approvals are off" — the wrong answer in the
  // dangerous direction.
  it("falls back to approvals-on when the read fails", async () => {
    const client = {
      getApprovalsSettings: vi.fn().mockRejectedValue(new Error("offline")),
      updateApprovalsSettings: vi.fn(),
    };
    const { getByTestId, queryByTestId } = render(() => (
      <ApprovalsOffPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("approvals-off-start")).toBeTruthy());
    expect(queryByTestId("approvals-off-active")).toBeNull();
  });

  it("surfaces a save failure and stays on", async () => {
    const client = {
      getApprovalsSettings: vi.fn().mockResolvedValue(approvals()),
      updateApprovalsSettings: vi.fn().mockRejectedValue(new Error("nope")),
    };
    const { getByTestId, queryByTestId } = render(() => (
      <ApprovalsOffPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("approvals-off-start")).toBeTruthy());
    fireEvent.click(getByTestId("approvals-off-start"));
    fireEvent.click(getByTestId("approvals-off-acknowledge"));
    fireEvent.click(getByTestId("approvals-off-confirm-button"));

    await waitFor(() =>
      expect(getByTestId("approvals-off-error").getAttribute("role")).toBe(
        "alert",
      ),
    );
    expect(queryByTestId("approvals-off-active")).toBeNull();
  });
});
