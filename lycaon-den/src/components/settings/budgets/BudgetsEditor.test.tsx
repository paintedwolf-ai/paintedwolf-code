import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { CostSummary, SettingsLimitsResponse } from "../../../api/types.ts";
import { NANO_PER_USD } from "../../../cost/nano-usd.ts";
import { createCostStore } from "../../../store/cost-store.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { BudgetsEditor } from "./BudgetsEditor.tsx";

const sampleLimits = (): SettingsLimitsResponse => ({
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
  spend_warning_ratio: 0.8,
  spend_ceiling_enabled: false,
  spend_soft_stop: true,
  merged_from: ["bundled"],
});

const pricedSummary = (usd: number): CostSummary =>
  ({
    scope: "session",
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

const unpricedSummary = (): CostSummary =>
  ({
    scope: "session",
    estimated_nano_usd: 0,
    estimate_coverage: "unpriced",
    token_totals: { prompt: 10, completion: 5 },
    coordinator: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 10, completion: 5 },
    },
    workers: {
      estimated_nano_usd: 0,
      token_totals: { prompt: 0, completion: 0 },
      task_count: 0,
    },
  }) as CostSummary;

describe("BudgetsEditor", () => {
  it("saves only the toggle while accepting unrelated host updates", async () => {
    const initial = sampleLimits();
    const settingsStore = createSettingsStore({ providers: [], limits: initial });
    const updateLimitsSettings = vi.fn(async (body) => ({ ...settingsStore.state.limits!, ...body }));
    render(() => <BudgetsEditor client={{ updateLimitsSettings } as never} settingsStore={settingsStore} />);
    fireEvent.click(await screen.findByTestId("spend-ceiling-enabled"));
    settingsStore.actions.setLimits({ ...initial, max_iterations: 700 });
    expect(screen.getByLabelText("Max iterations per turn")).toHaveProperty("value", "700");
    await waitFor(() => expect(updateLimitsSettings).toHaveBeenCalledWith({ spend_ceiling_enabled: true }));
    expect(settingsStore.state.limits?.max_iterations).toBe(700);
  });


  it("saves edits made while an earlier field update is in flight", async () => {
    let finishFirst!: (limits: SettingsLimitsResponse) => void;
    const updateLimitsSettings = vi.fn()
      .mockImplementationOnce(() => new Promise<SettingsLimitsResponse>((resolve) => { finishFirst = resolve; }))
      .mockImplementation(async (body) => ({ ...sampleLimits(), ...body }));
    const settingsStore = createSettingsStore({ providers: [], limits: sampleLimits() });
    render(() => <BudgetsEditor client={{ updateLimitsSettings } as never} settingsStore={settingsStore} />);
    const input = await screen.findByLabelText("Max iterations per turn");
    fireEvent.input(input, { target: { value: "600" } });
    await waitFor(() => expect(updateLimitsSettings).toHaveBeenCalledWith({ max_iterations: 600 }));
    fireEvent.input(input, { target: { value: "500" } });
    finishFirst({ ...sampleLimits(), max_iterations: 600, worker_tool_budget_default: 30 });
    await waitFor(() => expect(updateLimitsSettings).toHaveBeenNthCalledWith(2, { max_iterations: 500 }));
    expect(input).toHaveProperty("value", "500");
  });

  it("keeps the enabled ceiling unlocked while its save is acknowledged", async () => {
    let finishFirst!: (limits: SettingsLimitsResponse) => void;
    const updateLimitsSettings = vi.fn()
      .mockImplementationOnce(() => new Promise<SettingsLimitsResponse>((resolve) => { finishFirst = resolve; }))
      .mockImplementation(async (body) => ({ ...sampleLimits(), spend_ceiling_enabled: true, ...body }));
    const settingsStore = createSettingsStore({ providers: [], limits: sampleLimits() });
    render(() => <BudgetsEditor client={{ updateLimitsSettings } as never} settingsStore={settingsStore} />);
    fireEvent.click(await screen.findByTestId("spend-ceiling-enabled"));
    await waitFor(() => expect(updateLimitsSettings).toHaveBeenCalledWith({ spend_ceiling_enabled: true }));
    const input = screen.getByTestId("spend-ceiling-usd");
    const group = input.closest("fieldset")!;
    expect(group.disabled).toBe(false);
    input.focus();
    fireEvent.input(input, { target: { value: "12.5" } });
    // A disabled fieldset blurs the field being typed in.
    const locked: boolean[] = [];
    const observer = new MutationObserver(() => locked.push(group.disabled));
    observer.observe(group, { attributes: true, attributeFilter: ["disabled"] });
    finishFirst({ ...sampleLimits(), spend_ceiling_enabled: true });
    await waitFor(() => expect(updateLimitsSettings).toHaveBeenNthCalledWith(2, {
      session_spend_ceiling_nano_usd: Math.round(12.5 * NANO_PER_USD),
    }));
    observer.disconnect();
    expect(locked).toEqual([]);
    expect(document.activeElement).toBe(input);
  });

  it("keeps numeric inputs mounted and focused through edits and saves", async () => {
    const updateLimitsSettings = vi.fn().mockImplementation(async (body) => ({
      ...sampleLimits(), ...body,
    }));
    const settingsStore = createSettingsStore({ providers: [], limits: sampleLimits() });
    render(() => <BudgetsEditor client={{ updateLimitsSettings } as never} settingsStore={settingsStore} />);
    const input = await screen.findByLabelText("Max iterations per turn");
    input.focus();
    fireEvent.input(input, { target: { value: "501" } });
    expect(screen.getByLabelText("Max iterations per turn")).toBe(input);
    expect(document.activeElement).toBe(input);
    fireEvent.input(input, { target: { value: "5012" } });
    await waitFor(() => expect(updateLimitsSettings).toHaveBeenCalledWith(
      { max_iterations: 5012 },
    ));
    expect(screen.getByLabelText("Max iterations per turn")).toBe(input);
    expect(document.activeElement).toBe(input);
  });

  it("binds the spend ceiling controls", async () => {
    const updateLimitsSettings = vi.fn().mockResolvedValue(sampleLimits());
    const settingsStore = createSettingsStore({
      providers: [],
      limits: sampleLimits(),
    });
    render(() => (
      <BudgetsEditor
        client={{ updateLimitsSettings } as never}
        settingsStore={settingsStore}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("spend-ceiling-section")).toBeTruthy();
    });
    expect(screen.getByTestId("spend-ceiling-enabled")).toBeTruthy();
    expect(screen.getByTestId("spend-ceiling-usd")).toBeTruthy();
    expect(screen.getByTestId("spend-warning-percent")).toHaveProperty("value", "80");
    expect(screen.getByTestId("spend-soft-stop")).toHaveProperty("checked", true);
    expect(screen.getByText(/safety brake, not a budget/i)).toBeTruthy();
    expect(screen.getByTestId("spend-ceiling-readout").getAttribute("data-kind")).toBe(
      "off",
    );
  });

  it("locks governed limits under their switches", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
      limits: sampleLimits(),
    });
    render(() => (
      <BudgetsEditor
        client={{ updateLimitsSettings: vi.fn().mockResolvedValue(sampleLimits()) } as never}
        settingsStore={settingsStore}
      />
    ));

    const group = (testId: string) =>
      screen.getByTestId(testId).closest("fieldset");
    await waitFor(() => expect(group("spend-ceiling-usd")?.disabled).toBe(true));
    expect(group("spend-soft-stop")).toBe(group("spend-ceiling-usd"));
    expect(group("coordinator-loop-cycles")?.disabled).toBe(false);

    fireEvent.click(screen.getByTestId("coordinator-loop-enabled"));
    await waitFor(() =>
      expect(group("coordinator-loop-cycles")?.disabled).toBe(true),
    );
  });

  it("shows unpriced honesty when enabled and pricing is unavailable", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
      limits: { ...sampleLimits(), spend_ceiling_enabled: true },
    });
    const costStore = createCostStore();
    await costStore.refreshSession(
      { getCostSummary: vi.fn(async () => unpricedSummary()) } as never,
      "sess-1",
    );

    render(() => (
      <BudgetsEditor
        client={{ updateLimitsSettings: vi.fn() } as never}
        settingsStore={settingsStore}
        costStore={costStore}
      />
    ));

    await waitFor(() => {
      const readout = screen.getByTestId("spend-ceiling-readout");
      expect(readout.getAttribute("data-kind")).toBe("unpriced");
      expect(readout.textContent).toContain("not enforced (pricing unavailable)");
    });
  });

  it("shows spent of ceiling when priced", async () => {
    const settingsStore = createSettingsStore({
      providers: [],
      limits: {
        ...sampleLimits(),
        spend_ceiling_enabled: true,
        session_spend_ceiling_nano_usd: 10 * NANO_PER_USD,
      },
    });
    const costStore = createCostStore();
    await costStore.refreshSession(
      { getCostSummary: vi.fn(async () => pricedSummary(2.5)) } as never,
      "sess-1",
    );

    render(() => (
      <BudgetsEditor
        client={{ updateLimitsSettings: vi.fn() } as never}
        settingsStore={settingsStore}
        costStore={costStore}
      />
    ));

    await waitFor(() => {
      const readout = screen.getByTestId("spend-ceiling-readout");
      expect(readout.getAttribute("data-kind")).toBe("priced");
      expect(readout.textContent).toMatch(/spent .+ of .+/);
      expect(readout.getAttribute("data-ratio")).toBe("0.25");
    });
  });

  it("enables the USD input when the toggle is turned on", async () => {
    const updateLimitsSettings = vi.fn().mockImplementation(async (body) => ({
      ...sampleLimits(),
      ...body,
      scope: "global",
      merged_from: ["bundled"],
    }));
    const settingsStore = createSettingsStore({
      providers: [],
      limits: sampleLimits(),
    });
    render(() => (
      <BudgetsEditor
        client={{ updateLimitsSettings } as never}
        settingsStore={settingsStore}
      />
    ));

    const usdGroup = () =>
      screen.getByTestId("spend-ceiling-usd").closest("fieldset");
    await screen.findByTestId("spend-ceiling-usd");
    expect(usdGroup()?.disabled).toBe(true);

    fireEvent.click(screen.getByTestId("spend-ceiling-enabled"));
    await waitFor(() => {
      expect(usdGroup()?.disabled).toBe(false);
    });
  });
});
