import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { CostSummaryPanel } from "./CostSummaryPanel.tsx";
import type { CostSummary } from "../../api/types.ts";

const priced: CostSummary = {
  scope: "session",
  session_id: "s1",
  estimated_nano_usd: 420_000_000,
  token_totals: { prompt: 10, completion: 5 },
  coordinator: {
    estimated_nano_usd: 300_000_000,
    token_totals: { prompt: 8, completion: 3 },
  },
  workers: {
    estimated_nano_usd: 120_000_000,
    token_totals: { prompt: 2, completion: 2 },
  },
  summarizer: {
    estimated_nano_usd: 0,
    token_totals: { prompt: 0, completion: 0 },
  },
  estimate_coverage: "complete",
  pricing_provenance: [{ source: "models-dev" }],
};

const unpriced: CostSummary = { ...priced, estimate_coverage: "unpriced" };

describe("CostSummaryPanel", () => {

  it("updates cache comparisons without remounting the live summary", () => {
    const [summary, setSummary] = createSignal<CostSummary | undefined>({
      ...priced, token_totals: { prompt: 1000, completion: 20, cache_read: 500 },
      cache_savings: { estimated_nano_usd: 500_000_000, unpriced_tokens: 0 },
    });
    render(() => <CostSummaryPanel summary={summary()} />);
    const total = screen.getByTestId("cost-summary-total-usd");
    expect(screen.getByTestId("cost-cache-comparison").textContent).toContain("$0.50 less");
    setSummary({ ...summary()!, estimated_nano_usd: 2_000_000_000, cache_savings: { estimated_nano_usd: -250_000_000, unpriced_tokens: 0 } });
    expect(screen.getByTestId("cost-summary-total-usd")).toBe(total);
    expect(total.textContent).toBe("$2.00");
    expect(screen.getByTestId("cost-cache-comparison").textContent).toContain("$0.25 more");
    setSummary(undefined);
    expect(screen.queryByTestId("cost-cache-comparison")).toBeNull();
  });
  it("renders totals and estimate disclaimer", () => {
    render(() => (
      <CostSummaryPanel summary={priced} trackingSince="2025-07-01T12:00:00Z" />
    ));
    expect(screen.getByTestId("cost-summary-disclaimer").textContent).toMatch(
      /estimated/,
    );
    expect(screen.getByTestId("cost-summary-total-usd")).toBeTruthy();
    expect(
      screen.getByTestId("cost-summary-tracking-since").textContent,
    ).toMatch(/Tracking since/);
  });

  it("breaks tokens down in and out beside every cost row", () => {
    render(() => (
      <CostSummaryPanel
        summary={{
          ...priced,
          token_totals: { prompt: 9200, completion: 640 },
          coordinator: {
            estimated_nano_usd: 300_000_000,
            token_totals: { prompt: 8000, completion: 500 },
          },
        }}
      />
    ));
    expect(screen.getByTestId("cost-summary-tokens").textContent).toBe(
      "~9.2K in · 640 out",
    );
    expect(
      screen.getByTestId("cost-summary-coordinator-tokens").textContent,
    ).toBe("~8K in · 500 out");
    expect(screen.getByTestId("cost-summary-workers-tokens")).toBeTruthy();
    expect(screen.getByTestId("cost-summary-summarizer-tokens")).toBeTruthy();
  });

  it("keeps tokens visible when pricing is unavailable", () => {
    render(() => <CostSummaryPanel summary={unpriced} />);
    expect(screen.getByTestId("cost-summary-total-usd").textContent).toBe("—");
    expect(screen.queryByTestId("cost-summary-total-usd-coverage")).toBeNull();
    expect(screen.getByTestId("cost-summary-tokens").textContent).toBe(
      "10 in · 5 out",
    );
  });

  it("shows unpriced honesty", () => {
    render(() => <CostSummaryPanel summary={unpriced} />);
    expect(screen.getByTestId("cost-summary-unpriced").textContent).toMatch(
      /Pricing unavailable/,
    );
  });

  it("marks a lower bound beside the amount, never inside it", () => {
    render(() => (
      <CostSummaryPanel
        summary={{ ...priced, estimate_coverage: "lower_bound", unpriced_tokens: 1200 }}
      />
    ));
    expect(screen.getByTestId("cost-summary-total-usd").textContent).toBe("$0.42");
    expect(screen.getByTestId("cost-summary-total-usd-coverage").textContent).toBe(
      "Lower bound",
    );
    expect(screen.getByTestId("cost-summary-lower-bound").textContent).toMatch(
      /Lower bound — ~1\.2K tokens unpriced\. Actual charges may be higher\./,
    );
    expect(screen.queryByTestId("cost-summary-unpriced")).toBeNull();
  });

  it("keeps a sub-cent lower bound as an amount and a separate mark", () => {
    render(() => (
      <CostSummaryPanel
        summary={{
          ...priced,
          estimated_nano_usd: 3_100_000,
          estimate_coverage: "lower_bound",
          unknown_calls: 1,
          unknown_charged_calls: 1,
        }}
      />
    ));
    expect(screen.getByTestId("cost-summary-total-usd").textContent).toBe("<$0.01");
    expect(screen.getByTestId("cost-summary-total-usd-coverage").textContent).toBe(
      "Lower bound",
    );
    expect(screen.getByTestId("cost-summary-lower-bound").textContent).toMatch(
      /1 provider call with incomplete usage/,
    );
  });

  it("lists both lower-bound causes and separates free local calls", () => {
    render(() => (
      <CostSummaryPanel
        summary={{
          ...priced,
          estimate_coverage: "lower_bound",
          unpriced_tokens: 40,
          unknown_calls: 3,
          unknown_charged_calls: 1,
        }}
      />
    ));
    expect(screen.getByTestId("cost-summary-lower-bound").textContent).toMatch(
      /40 tokens unpriced; 1 provider call with incomplete usage/,
    );
    expect(
      screen.getByTestId("cost-summary-unknown-local-calls").textContent,
    ).toMatch(/2 local calls with incomplete usage/);
  });

  it("says nothing about a lower bound when only local calls went unreported", () => {
    render(() => <CostSummaryPanel summary={{ ...priced, unknown_calls: 2 }} />);
    expect(screen.queryByTestId("cost-summary-lower-bound")).toBeNull();
    expect(screen.queryByTestId("cost-summary-total-usd-coverage")).toBeNull();
    expect(
      screen.getByTestId("cost-summary-unknown-local-calls").textContent,
    ).toMatch(/2 local calls with incomplete usage — those tokens are missing from the counts, but local inference costs nothing/);
  });

  it("discloses host-measured tokens as included and approximate", () => {
    render(() => (
      <CostSummaryPanel summary={{ ...priced, host_measured_tokens: 640 }} />
    ));
    expect(screen.getByTestId("cost-summary-host-measured").textContent).toMatch(
      /counted by the host after a call ended without a usage report/,
    );
    expect(screen.getByTestId("cost-summary-disclaimer").textContent).toMatch(
      /Figures are estimated —/,
    );
  });

  it("shows the empty state before a summary loads", () => {
    render(() => <CostSummaryPanel loading />);
    expect(screen.getByTestId("cost-summary-empty").textContent).toMatch(
      /Loading estimated cost/,
    );
  });
});
