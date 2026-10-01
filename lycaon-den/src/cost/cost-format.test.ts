import { describe, expect, it } from "vitest";
import type { CostSummary } from "../api/types.ts";
import { costExportFilename } from "./cost-export.ts";
import {
  costChipView,
  estimateView,
  formatCeilingUSD,
  formatModelTokenRates,
  formatTokenSplit,
  formatTokenSplitExact,
  formatTokens,
  formatTokensCompact,
  formatUSD,
  hostMeasuredTokens,
  joinReasons,
  priced,
  sourceAsOfLabel,
  trackingSinceLabel,
  unreportedChargedCalls,
  unreportedFreeCalls,
} from "./cost-format.ts";
import { costTrackingEnabled, costTrackingSince } from "./cost-tracking.ts";
import { createSettingsStore } from "../store/settings-store.ts";

const summary: CostSummary = {
  scope: "project",
  project_id: "proj-1",
  estimated_nano_usd: 1_250_000_000,
  token_totals: { prompt: 1000, completion: 200 },
  coordinator: {
    estimated_nano_usd: 750_000_000,
    token_totals: { prompt: 600, completion: 100 },
  },
  workers: {
    estimated_nano_usd: 500_000_000,
    token_totals: { prompt: 400, completion: 100 },
    task_count: 2,
  },
  summarizer: {
    estimated_nano_usd: 0,
    token_totals: { prompt: 0, completion: 0 },
  },
  estimate_coverage: "complete",
  pricing_provenance: [
    { source: "models-dev", priced_at: "2025-07-01T12:00:00Z" },
  ],
};

const lowerBound = (causes: Partial<CostSummary>): CostSummary => ({
  ...summary,
  estimate_coverage: "lower_bound",
  ...causes,
});

describe("cost-tracking", () => {
  it("reads enabled and since from settings pricing", () => {
    const store = createSettingsStore({
      providers: [],
      pricing: {
        cost_tracking_enabled: true,
        cost_tracking_since_at: "2025-07-01T12:00:00Z",
        sources: [],
        available_sources: [],
      },
    });
    expect(costTrackingEnabled(store)).toBe(true);
    expect(costTrackingSince(store)).toBe("2025-07-01T12:00:00Z");
  });

  it("is off when pricing missing", () => {
    const store = createSettingsStore();
    expect(costTrackingEnabled(store)).toBe(false);
    expect(costTrackingSince(store)).toBeNull();
  });
});

describe("cost-format", () => {
  it("formats usd and tokens", () => {
    expect(formatUSD(1.5)).toMatch(/1\.50|1\.5/);
    expect(formatTokens(1200)).toBe((1200).toLocaleString());
  });

  it("never shows usd past cents", () => {
    expect(formatUSD(1.23456)).toBe("$1.23");
    expect(formatUSD(0.00042)).toBe("<$0.01");
    expect(formatUSD(0)).toBe("$0.00");
  });

  it("rounds tokens to one short word", () => {
    expect(formatTokensCompact(0)).toBe("0");
    expect(formatTokensCompact(820)).toBe("820");
    expect(formatTokensCompact(1000)).toBe("~1K");
    expect(formatTokensCompact(1520)).toBe("~1.5K");
    expect(formatTokensCompact(124_523)).toBe("~125K");
    expect(formatTokensCompact(999_800)).toBe("~1M");
    expect(formatTokensCompact(1_500_000)).toBe("~1.5M");
    expect(formatTokensCompact(2_400_000_000)).toBe("~2.4B");
    expect(formatTokensCompact(undefined)).toBe("—");
  });

  it("splits tokens into in and out, rounded or exact", () => {
    expect(formatTokenSplit({ prompt: 9200, completion: 640 })).toBe(
      "~9.2K in · 640 out",
    );
    expect(formatTokenSplitExact({ prompt: 9200, completion: 640 })).toBe(
      `${(9200).toLocaleString()} in · 640 out`,
    );
  });

  it("formats model rates as trimmed per-1M USD", () => {
    expect(formatModelTokenRates(0.01, 0.02)).toBe("($10/$20 per 1M)");
    expect(formatModelTokenRates(0.0025, 0.01)).toBe("($2.50/$10 per 1M)");
    expect(formatModelTokenRates(0.00015, 0.0006)).toBe(
      "($0.15/$0.60 per 1M)",
    );
    expect(formatModelTokenRates(0, 0)).toBe("($0/$0 per 1M)");
    expect(formatModelTokenRates(0.0000015, 0.000006)).toBe(
      "($0.0015/$0.006 per 1M)",
    );
    expect(formatModelTokenRates(0.15, 0.6)).toBe("($150/$600 per 1M)");
  });

  it("includes cache subsets without adding them to input totals", () => {
    expect(formatTokenSplitExact({ prompt: 100, completion: 20, cache_read: 50, cache_write: 30 }))
      .toBe("100 in · 20 out · 50 cache read · 30 cache written");
  });

  it("reads priced from the host coverage, not from the counters", () => {
    expect(priced(summary)).toBe(true);
    expect(priced(lowerBound({ unpriced_tokens: 1 }))).toBe(true);
    expect(priced({ ...summary, estimate_coverage: "unpriced" })).toBe(false);
    expect(priced(undefined)).toBe(false);
    expect(sourceAsOfLabel({ ...summary, estimate_coverage: "unpriced" })).toBe(
      "pricing unavailable",
    );
    expect(sourceAsOfLabel(summary)).toContain("models-dev");
    expect(
      sourceAsOfLabel({
        ...summary,
        pricing_provenance: [
          { source: "live", priced_at: "2026-01-02T00:00:00Z" },
          { source: "models-dev", priced_at: "2025-07-01T12:00:00Z" },
        ],
      }),
    ).toContain("live");
    expect(trackingSinceLabel("2025-07-01T12:00:00Z")).toMatch(/Tracking since/);
  });

  it("separates unreported charged calls from free local ones", () => {
    expect(unreportedChargedCalls({ ...summary, unknown_calls: 3, unknown_charged_calls: 1 })).toBe(1);
    expect(unreportedFreeCalls({ ...summary, unknown_calls: 3, unknown_charged_calls: 1 })).toBe(2);
    expect(unreportedFreeCalls({ ...summary, unknown_calls: 2 })).toBe(2);
    expect(hostMeasuredTokens({ ...summary, host_measured_tokens: 640 })).toBe(640);
  });

  it("joins reasons for prose", () => {
    expect(joinReasons([])).toBe("");
    expect(joinReasons(["a"])).toBe("a");
    expect(joinReasons(["a", "b"])).toBe("a and b");
    expect(joinReasons(["a", "b", "c"])).toBe("a, b and c");
  });
});

describe("estimateView", () => {
  it("renders a complete estimate as a bare amount", () => {
    expect(estimateView(summary)).toEqual({
      coverage: "complete",
      amount: "$1.25",
      phrase: "$1.25",
      reasons: [],
    });
  });

  it("keeps the amount bare and carries the coverage beside it", () => {
    const view = estimateView(lowerBound({ unpriced_tokens: 1500, unknown_calls: 3, unknown_charged_calls: 1 }));
    expect(view.coverage).toBe("lower_bound");
    expect(view.amount).toBe("$1.25");
    expect(view.qualifier).toBe("Lower bound");
    expect(view.phrase).toBe("at least $1.25");
    expect(view.reasons).toEqual(["~1.5K tokens unpriced", "1 provider call with incomplete usage"]);
  });

  it("says what is known when a lower bound rounds below a cent", () => {
    // The amount stays a rounded figure; the sentence states the priced portion.
    const view = estimateView(lowerBound({ estimated_nano_usd: 3_100_000, unknown_calls: 1, unknown_charged_calls: 1 }));
    expect(view.amount).toBe("<$0.01");
    expect(view.qualifier).toBe("Lower bound");
    expect(view.phrase).toBe("less than $0.01 in priced usage");
    expect(estimateView(lowerBound({ estimated_nano_usd: 0, unknown_charged_calls: 1 })).phrase).toBe(
      "less than $0.01 in priced usage",
    );
  });

  it("has no amount when nothing is priced", () => {
    expect(estimateView({ ...summary, estimate_coverage: "unpriced" })).toEqual({
      coverage: "unpriced",
      qualifier: "Pricing unavailable",
      phrase: "unpriced",
      reasons: [],
    });
    expect(estimateView(undefined).coverage).toBe("unpriced");
  });

  it("trusts the host: counters alone never make a lower bound", () => {
    const view = estimateView({ ...summary, unknown_calls: 2, host_measured_tokens: 640 });
    expect(view.coverage).toBe("complete");
    expect(view.qualifier).toBeUndefined();
  });
});

describe("cost-export", () => {
  it("names export files with project slug and date", () => {
    expect(costExportFilename("My App!", "csv")).toMatch(
      /^my-app-cost-\d{8}\.csv$/,
    );
  });
});

describe("costChipView", () => {
  const session: CostSummary = {
    ...summary,
    scope: "session",
    session_id: "s1",
    estimated_nano_usd: 420_000_000,
  };

  it("returns the plain label when the ceiling is off", () => {
    const view = costChipView(session, undefined, {
      enabled: false,
      ceilingUsd: 5,
    });
    expect(view?.label).toBe(formatUSD(0.42));
    expect(view?.coverage).toBe("complete");
    expect(view?.ceiling).toBeUndefined();
  });

  it("folds an enabled ceiling into the label with ratio", () => {
    const view = costChipView(session, undefined, {
      enabled: true,
      ceilingUsd: 5,
    });
    expect(view?.label).toBe(`${formatUSD(0.42)} / ${formatCeilingUSD(5)}`);
    expect(view?.ceiling?.ratio).toBeCloseTo(0.42 / 5);
  });

  it("returns no view for unpriced sessions even with a ceiling", () => {
    const view = costChipView(
      { ...session, estimate_coverage: "unpriced" },
      { ...session, estimated_nano_usd: 3_100_000_000 },
      { enabled: true, ceilingUsd: 5 },
    );
    expect(view).toBeUndefined();
  });

  it("keeps a lower-bound summary visible with the coverage beside the amount", () => {
    // Incomplete usage preserves the priced amount and ceiling ratio.
    const unreported = { ...session, estimate_coverage: "lower_bound" as const, unknown_calls: 3, unknown_charged_calls: 3 };
    const view = costChipView(unreported, undefined, {
      enabled: true,
      ceilingUsd: 5,
    });
    expect(view?.label).toBe(`${formatUSD(0.42)} / ${formatCeilingUSD(5)}`);
    expect(view?.coverage).toBe("lower_bound");
    expect(view?.ceiling?.ratio).toBeCloseTo(0.42 / 5);
  });

  it("renders a sub-cent lower bound as an amount with coverage, never as ≥<$0.01", () => {
    const view = costChipView(
      { ...session, estimated_nano_usd: 3_100_000, estimate_coverage: "lower_bound", unknown_calls: 1, unknown_charged_calls: 1 },
      undefined,
    );
    expect(view?.label).toBe("<$0.01");
    expect(view?.coverage).toBe("lower_bound");
    expect(view?.label).not.toContain("≥");
  });

  it("preserves board pricing coverage while the detailed summary loads", () => {
    const partial = { ...session, estimated_nano_usd: 3_100_000_000, estimate_coverage: "lower_bound" as const, unpriced_tokens: 100 };
    const view = costChipView(undefined, partial, { enabled: true, ceilingUsd: 5 });
    expect(view?.label).toBe(`${formatUSD(3.1)} / ${formatCeilingUSD(5)}`);
    expect(view?.coverage).toBe("lower_bound");
    expect(view?.ceiling?.ratio).toBeCloseTo(3.1 / 5);
    expect(costChipView(undefined, { ...session, estimate_coverage: "unpriced" })).toBeUndefined();
    expect(costChipView(undefined, null)).toBeUndefined();
  });

  it("formats whole-dollar ceilings without cents", () => {
    expect(formatCeilingUSD(5)).toBe("$5");
    expect(formatCeilingUSD(2.5)).toBe("$2.50");
  });
});
