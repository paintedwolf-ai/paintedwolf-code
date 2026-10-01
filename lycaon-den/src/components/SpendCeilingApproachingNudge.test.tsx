import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  CostSummary,
  SettingsLimitsResponse,
} from "../api/types.ts";
import { formatUSD } from "../cost/cost-format.ts";
import { NANO_PER_USD } from "../cost/nano-usd.ts";
import {
  resetSpendCeilingApproachingDismissalsForTest,
  SpendCeilingApproachingNudge,
} from "./SpendCeilingApproachingNudge.tsx";

const limitsDoc = (ceiling = 5): SettingsLimitsResponse => ({
  scope: "global",
  max_iterations: 500,
  overlay_promote_max_iterations: 150,
  max_tool_result_bytes: 131072,
  coordinator_loop: true,
  max_coordinator_loop_cycles: 64,
  llm_turn_timeout_ms: 10800000,
  coordinator_host_turn_timeout_ms: 10800000,
  coordinator_max_sleep_ms: 10800000,
  await_parent_workers_timeout_ms: 10800000,
  worker_tool_budget_default: 20,
  worker_tool_budget_min: 2,
  worker_tool_budget_max: 120,
  session_spend_ceiling_nano_usd: ceiling * NANO_PER_USD,
  spend_ceiling_enabled: true,
  spend_soft_stop: true,
  merged_from: ["bundled"],
});

const pricedSummary = (usd: number): CostSummary =>
  ({
    scope: "session",
    session_id: "sess-1",
    estimated_nano_usd: Math.round(usd * 1e9),
    estimate_coverage: "complete",
    token_totals: { prompt: 1, completion: 1 },
    coordinator: {
      estimated_nano_usd: Math.round(usd * 1e9),
      token_totals: { prompt: 1, completion: 1 },
    },
    workers: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 0, completion: 0 },
      task_count: 0,
    },
  }) as CostSummary;

beforeEach(() => {
  resetSpendCeilingApproachingDismissalsForTest();
});

describe("SpendCeilingApproachingNudge", () => {
  it.each(["read", "write"] as const)("keeps the warning actionable after a failed %s", async (stage) => {
    const getLimitsSettings = vi.fn().mockResolvedValue(limitsDoc());
    const updateLimitsSettings = vi.fn().mockResolvedValue(limitsDoc(10));
    (stage === "read" ? getLimitsSettings : updateLimitsSettings).mockRejectedValueOnce(new Error("Connection interrupted"));
    const onLimitsUpdated = vi.fn();
    render(() => <SpendCeilingApproachingNudge
      client={{ getLimitsSettings, updateLimitsSettings } as never}
      sessionId="sess-1" projectId="proj-1" summary={pricedSummary(4.25)}
      limits={limitsDoc()} onLimitsUpdated={onLimitsUpdated} onOpenBudgets={vi.fn()}
    />);

    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    await screen.findByRole("alert");
    expect((screen.getByTestId("system-nudge-primary") as HTMLButtonElement).disabled).toBe(false);
    expect(onLimitsUpdated).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    await waitFor(() => expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull());
    expect(onLimitsUpdated).toHaveBeenCalledOnce();
  });

  it("renders when armed, priced, and past 80% with both amounts", async () => {
    render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(4.25)}
        limits={limitsDoc(5)}
        onOpenBudgets={vi.fn()}
      />
    ));
    const nudge = await screen.findByTestId("spend-ceiling-approaching-nudge");
    expect(nudge.textContent).toContain("Approaching the spend ceiling");
    expect(nudge.textContent).toContain(formatUSD(4.25));
    expect(nudge.textContent).toContain(formatUSD(5));
  });

  it("discloses a lower bound with its causes", async () => {
    render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={{ ...pricedSummary(4.25), estimate_coverage: "lower_bound", unpriced_tokens: 20 }}
        limits={limitsDoc(5)}
        onOpenBudgets={vi.fn()}
      />
    ));
    const nudge = await screen.findByTestId("spend-ceiling-approaching-nudge");
    expect(nudge.textContent).toContain(`spent at least ${formatUSD(4.25)}`);
    expect(nudge.textContent).toContain("That is a lower bound: 20 tokens unpriced.");
  });

  it("renders nothing while limits are still loading", () => {
    render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(4.25)}
        limits={null}
        onOpenBudgets={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull();
  });

  it("raises the ceiling via PUT and dismisses the card", async () => {
    const getLimitsSettings = vi.fn()
      .mockResolvedValueOnce(limitsDoc(5))
      .mockResolvedValueOnce(limitsDoc(5))
      .mockResolvedValue(limitsDoc(10));
    const updateLimitsSettings = vi.fn().mockResolvedValue({
      ...limitsDoc(5),
      session_spend_ceiling_nano_usd: 10 * NANO_PER_USD,
    });
    const onLimitsUpdated = vi.fn();
    render(() => (
      <SpendCeilingApproachingNudge
        client={{ getLimitsSettings, updateLimitsSettings } as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(4.25)}
        limits={limitsDoc(5)}
        onLimitsUpdated={onLimitsUpdated}
        onOpenBudgets={vi.fn()}
      />
    ));
    await screen.findByTestId("spend-ceiling-approaching-nudge");
    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    await waitFor(() => {
      expect(getLimitsSettings).toHaveBeenCalledWith();
      expect(getLimitsSettings).toHaveBeenCalledWith("proj-1");
      expect(updateLimitsSettings).toHaveBeenCalled();
      expect(onLimitsUpdated).toHaveBeenCalled();
    });
    await waitFor(() =>
      expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull(),
    );
    const [body, projectId] = updateLimitsSettings.mock.calls[0]!;
    expect((body as SettingsLimitsResponse).session_spend_ceiling_nano_usd).toBe(10 * NANO_PER_USD);
    expect(projectId).toBeUndefined();
  });

  it("keeps dismissal for the same ceiling across re-renders", async () => {
    const onOpenBudgets = vi.fn();
    const first = render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(4.25)}
        limits={limitsDoc(5)}
        onOpenBudgets={onOpenBudgets}
      />
    ));
    await screen.findByTestId("spend-ceiling-approaching-nudge");
    fireEvent.click(screen.getByTestId("system-nudge-dismiss"));
    await waitFor(() =>
      expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull(),
    );
    first.unmount();

    render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(4.8)}
        limits={limitsDoc(5)}
        onOpenBudgets={onOpenBudgets}
      />
    ));
    expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull();
  });

  it("re-arms when the ceiling value changes", async () => {
    const view = render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(4.25)}
        limits={limitsDoc(5)}
        onOpenBudgets={vi.fn()}
      />
    ));
    await screen.findByTestId("spend-ceiling-approaching-nudge");
    fireEvent.click(screen.getByTestId("system-nudge-dismiss"));
    await waitFor(() =>
      expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull(),
    );
    view.unmount();

    render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(16)}
        limits={limitsDoc(20)}
        onOpenBudgets={vi.fn()}
      />
    ));
    expect(
      await screen.findByTestId("spend-ceiling-approaching-nudge"),
    ).toBeTruthy();
  });

  it("never renders at ratio 1 (reached controls that moment)", () => {
    render(() => (
      <SpendCeilingApproachingNudge
        client={{} as never}
        sessionId="sess-1"
        projectId="proj-1"
        summary={pricedSummary(5)}
        limits={limitsDoc(5)}
        onOpenBudgets={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("spend-ceiling-approaching-nudge")).toBeNull();
  });

  it("does not persist dismissal into app-state surfaces", () => {
    const root = join(import.meta.dirname, "..", "..");
    for (const rel of [
      "shared/app-state-types.ts",
      "src-tauri/src/lib.rs",
      "src/platform/persistence/app-state-parse.ts",
    ]) {
      const body = readFileSync(join(root, rel), "utf8");
      for (const needle of [
        "spendCeilingApproaching",
        "spend_ceiling_approaching",
        "approachingDismiss",
        "spendRunwayFired",
        "SPEND_CEILING_WARN",
      ]) {
        expect(body, `${rel} must not mention ${needle}`).not.toContain(needle);
      }
    }
  });
});
