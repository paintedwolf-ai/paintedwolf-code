import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import type { CostSummary, SettingsLimitsResponse } from "../api/types.ts";
import { createNoticeStore } from "../notices/notice-store.ts";
import { sessionScope } from "../notices/notice-scope.ts";
import { NANO_PER_USD } from "../cost/nano-usd.ts";
import { SpendCeilingReachedNudge } from "./SpendCeilingReachedNudge.tsx";

const limitsDoc = (): SettingsLimitsResponse => ({
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
  session_spend_ceiling_nano_usd: 5 * NANO_PER_USD,
  spend_ceiling_enabled: true,
  spend_soft_stop: true,
  merged_from: ["bundled"],
});

describe("SpendCeilingReachedNudge", () => {
  it.each(["before", "during", "removed"])("raises without submitting when unsent content exists %s the request", async (timing) => {
    const notices = createNoticeStore();
    notices.publish({ code: "session_spend_ceiling_reached", title: "Spend ceiling reached", message: "Work is paused." }, sessionScope("proj-1", "sess-1"));
    const [hasUnsentContent, setHasUnsentContent] = createSignal(timing !== "during");
    let resolveSummary!: (summary: Pick<CostSummary, "estimated_nano_usd">) => void;
    const getCostSummary = vi.fn(() => new Promise<Pick<CostSummary, "estimated_nano_usd">>((resolve) => { resolveSummary = resolve; }));
    let limits = limitsDoc();
    const updateLimitsSettings = vi.fn(async (body: Pick<SettingsLimitsResponse, "session_spend_ceiling_nano_usd" | "spend_ceiling_enabled">) => {
      limits = { ...limits, ...body };
      return limits;
    });
    const onResume = vi.fn();
    render(() => <SpendCeilingReachedNudge
      client={{ getCostSummary, getLimitsSettings: vi.fn(async () => limits), updateLimitsSettings } as never}
      notices={notices} sessionId="sess-1" projectId="proj-1"
      hasUnsentContent={hasUnsentContent()} onResume={onResume} onOpenBudgets={vi.fn()}
    />);
    const button = screen.getByTestId("system-nudge-primary");
    expect(button.textContent).toBe(timing === "during" ? "Raise ceiling & resume" : "Raise ceiling");
    fireEvent.click(button);
    setHasUnsentContent(timing !== "removed");
    resolveSummary({ estimated_nano_usd: 5 * NANO_PER_USD });
    await waitFor(() => expect(screen.queryByTestId("spend-ceiling-reached-nudge")).toBeNull());
    expect(updateLimitsSettings).toHaveBeenCalledOnce();
    expect(limits.session_spend_ceiling_nano_usd).toBeGreaterThan(5 * NANO_PER_USD);
    expect(onResume).not.toHaveBeenCalled();
  });
  it.each(["summary", "limits", "update", "resume", "refused"] as const)("retains a retry after %s fails without repeating a completed raise", async (stage) => {
    const notices = createNoticeStore();
    notices.publish({
      code: "session_spend_ceiling_reached", title: "Spend ceiling reached", message: "Work is paused.",
    }, sessionScope("proj-1", "sess-1"));
    const getCostSummary = vi.fn().mockResolvedValue({ estimated_nano_usd: 5 * NANO_PER_USD } satisfies Pick<CostSummary, "estimated_nano_usd">);
    const getLimitsSettings = vi.fn().mockResolvedValue(limitsDoc());
    const updateLimitsSettings = vi.fn().mockResolvedValue(limitsDoc());
    const onResume = vi.fn().mockResolvedValue(undefined);
    const operations = { summary: getCostSummary, limits: getLimitsSettings, update: updateLimitsSettings, resume: onResume };
    if (stage === "refused") onResume.mockResolvedValueOnce(false);
    else operations[stage].mockRejectedValueOnce(new Error("Connection interrupted"));
    render(() => <SpendCeilingReachedNudge
      client={{ getCostSummary, getLimitsSettings, updateLimitsSettings } as never}
      notices={notices} sessionId="sess-1" projectId="proj-1"
      onResume={onResume} onOpenBudgets={vi.fn()}
    />);

    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    await screen.findByRole("alert");
    expect(screen.queryByTestId("spend-ceiling-reached-nudge")).not.toBeNull();
    expect((screen.getByTestId("system-nudge-primary") as HTMLButtonElement).disabled).toBe(false);
    const raised = stage === "resume" || stage === "refused";
    if (!raised) expect(onResume).not.toHaveBeenCalled();
    const writesBeforeRetry = updateLimitsSettings.mock.calls.length;
    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    await waitFor(() => expect(screen.queryByTestId("spend-ceiling-reached-nudge")).toBeNull());
    expect(onResume).toHaveBeenCalledTimes(raised ? 2 : 1);
    if (raised) expect(updateLimitsSettings).toHaveBeenCalledTimes(writesBeforeRetry);
  });

  it("stays hidden without a spend-ceiling notice", () => {
    const notices = createNoticeStore();
    render(() => (
      <SpendCeilingReachedNudge
        client={{} as never}
        notices={notices}
        sessionId="sess-1"
        projectId="proj-1"
        onResume={vi.fn()}
        onOpenBudgets={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("spend-ceiling-reached-nudge")).toBeNull();
  });

  it("renders the notice and opens Budgets from the secondary action", async () => {
    const notices = createNoticeStore();
    notices.publish(
      {
        code: "session_spend_ceiling_reached",
        title: "Spend ceiling reached",
        message: "This session stopped at its spend ceiling ($5.00).",
      },
      sessionScope("proj-1", "sess-1"),
    );
    const onOpenBudgets = vi.fn();
    render(() => (
      <SpendCeilingReachedNudge
        client={{} as never}
        notices={notices}
        sessionId="sess-1"
        projectId="proj-1"
        onResume={vi.fn()}
        onOpenBudgets={onOpenBudgets}
      />
    ));

    const nudge = await screen.findByTestId("spend-ceiling-reached-nudge");
    expect(nudge.textContent).toContain("Spend ceiling reached");
    fireEvent.click(screen.getByTestId("system-nudge-secondary"));
    expect(onOpenBudgets).toHaveBeenCalledOnce();
  });

  it.each([0, 4.5])("refreshes a cached estimate of %s and publishes the raised ceiling before resuming", async (cachedSpend) => {
    const getCostSummary = vi.fn().mockResolvedValue({ estimated_nano_usd: 12_000_000_000 });
    const getLimitsSettings = vi.fn()
      .mockResolvedValueOnce(limitsDoc())
      .mockResolvedValueOnce(limitsDoc())
      .mockResolvedValue({ ...limitsDoc(), session_spend_ceiling_nano_usd: 13 * NANO_PER_USD });
    const updateLimitsSettings = vi.fn().mockResolvedValue({
      ...limitsDoc(),
      session_spend_ceiling_nano_usd: 13 * NANO_PER_USD,
    });
    const onLimitsUpdated = vi.fn();
    const onResume = vi.fn(async () => {
      expect(onLimitsUpdated).toHaveBeenCalledWith({
        scope: "global",
        limits: expect.objectContaining({ session_spend_ceiling_nano_usd: 13 * NANO_PER_USD }),
      });
    });
    const notices = createNoticeStore();
    notices.publish(
      {
        code: "session_spend_ceiling_reached",
        title: "Spend ceiling reached",
        message: "This session stopped at its spend ceiling ($5.00).",
      },
      sessionScope("proj-1", "sess-1"),
    );

    render(() => (
      <SpendCeilingReachedNudge
        client={{ getCostSummary, getLimitsSettings, updateLimitsSettings } as never}
        notices={notices}
        sessionId="sess-1"
        projectId="proj-1"
        spentUsd={cachedSpend}
        onResume={onResume}
        onLimitsUpdated={onLimitsUpdated}
        onOpenBudgets={vi.fn()}
      />
    ));

    await screen.findByTestId("spend-ceiling-reached-nudge");
    fireEvent.click(screen.getByTestId("system-nudge-primary"));

    await waitFor(() => {
      expect(getLimitsSettings).toHaveBeenCalledWith();
      expect(getLimitsSettings).toHaveBeenCalledWith("proj-1");
      expect(updateLimitsSettings).toHaveBeenCalled();
      expect(onResume).toHaveBeenCalledOnce();
      expect(getCostSummary).toHaveBeenCalledWith("sess-1");
    });

    expect(updateLimitsSettings.mock.calls.length).toBeGreaterThan(0);
    const firstCall = updateLimitsSettings.mock.calls[0];
    if (!firstCall) {
      throw new Error("expected updateLimitsSettings to be called");
    }
    const [body, projectId] = firstCall;
    expect((body as SettingsLimitsResponse).spend_ceiling_enabled).toBe(true);
    expect((body as SettingsLimitsResponse).session_spend_ceiling_nano_usd).toBe(13 * NANO_PER_USD);
    expect(projectId).toBeUndefined();
    await waitFor(() =>
      expect(screen.queryByTestId("spend-ceiling-reached-nudge")).toBeNull(),
    );
  });
});
