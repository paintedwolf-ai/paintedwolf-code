import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { stubClient } from "../../../test/client-fixture.ts";
import type { ApprovalRecentAsksResponse } from "../../../api/types.ts";
import { RecentAsksStrip } from "./RecentAsksStrip.tsx";

function clientWith(rows: ApprovalRecentAsksResponse["asks"]) {
  const listApprovalAsks = vi.fn(async () => ({
    window_days: 7,
    since_at: "2026-08-30T00:00:00Z",
    asks: rows,
  }));
  const createApprovalGrant = vi.fn(async () => ({}) as never);
  return {
    client: stubClient({ listApprovalAsks, createApprovalGrant }),
    listApprovalAsks,
    createApprovalGrant,
  };
}

describe("RecentAsksStrip", () => {
  it("names each reason with its counts and marks a reason allowed every time", async () => {
    const { client } = clientWith([
      { gate: "agent_chosen_outbound", asks: 14, allowed: 14, denied: 0, subjects: ["Allow network: api.github.com:443"] },
      { gate: "secret_outbound", asks: 4, allowed: 3, denied: 1 },
    ]);
    render(() => <RecentAsksStrip client={client} />);
    const rows = await screen.findAllByTestId("saved-approvals-recent-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain("The agent chose this destination, not your setup");
    expect(rows[0]?.textContent).toContain("14 asks · 14 allowed");
    expect(rows[0]?.querySelector("[data-testid=saved-approvals-recent-all-allowed]")).not.toBeNull();
    expect(rows[1]?.querySelector("[data-testid=saved-approvals-recent-all-allowed]")).toBeNull();
  });

  it("offers the device host lease only where every subject was one site, and saves it", async () => {
    const { client, createApprovalGrant } = clientWith([
      { gate: "agent_chosen_outbound", asks: 3, allowed: 3, denied: 0, host_pattern: "*.github.com:443" },
      { gate: "outside_roots_write", asks: 2, allowed: 2, denied: 0 },
    ]);
    render(() => <RecentAsksStrip client={client} />);
    const buttons = await screen.findAllByTestId("saved-approvals-recent-widen");
    expect(buttons).toHaveLength(1);
    expect(buttons[0]?.textContent).toBe("Allow *.github.com:443 on this device for 30 days");
    fireEvent.click(buttons[0]!);
    await waitFor(() => expect(createApprovalGrant).toHaveBeenCalledWith({
      category: "host",
      scope: "device",
      host_pattern: "*.github.com:443",
    }));
    await screen.findByTestId("saved-approvals-recent-widened");
  });

  it("says so when nothing asked", async () => {
    const { client } = clientWith([]);
    render(() => <RecentAsksStrip client={client} />);
    await screen.findByTestId("saved-approvals-recent-empty");
  });
});
