import { describe, expect, it } from "vitest";
import type { PricingSourceMeta, SettingsPricingSource } from "../../api/types.ts";
import {
  freshnessLabel,
  hasSelectedSource,
  selectPricingSource,
  statusBadge,
  withPendingPricing,
} from "./cost-sources-model.ts";

const sources: SettingsPricingSource[] = [
  { id: "models-dev", enabled: true },
  { id: "litellm", enabled: false },
  { id: "ai-pricing-fyi", enabled: false },
];

function meta(
  partial: Partial<PricingSourceMeta> & Pick<PricingSourceMeta, "id">,
): PricingSourceMeta {
  return {
    label: partial.id,
    kind: partial.id,
    enabled: false,
    status: "offline",
    ...partial,
  };
}

describe("cost-sources-model", () => {
  it("selectPricingSource replaces the active source", () => {
    const next = selectPricingSource(sources, "litellm");
    expect(next.find((s) => s.id === "litellm")?.enabled).toBe(true);
    expect(next.find((s) => s.id === "models-dev")?.enabled).toBe(false);
  });

  it("statusBadge maps feed status to label and tone", () => {
    expect(statusBadge(meta({ id: "a", status: "ok" }))).toEqual({
      label: "OK",
      tone: "ok",
    });
    expect(statusBadge(meta({ id: "a", status: "stale" })).tone).toBe("stale");
    expect(statusBadge(meta({ id: "a", status: "error" })).label).toBe("Error");
    expect(statusBadge(meta({ id: "a", status: "offline" })).tone).toBe(
      "offline",
    );
  });

  it("freshnessLabel prefers last_updated then fetched_at then never", () => {
    expect(freshnessLabel(meta({ id: "a" }))).toBe("never");
    const fetched = freshnessLabel(
      meta({ id: "a", fetched_at: "2025-07-01T12:00:00Z" }),
    );
    expect(fetched).not.toBe("never");
    const last = freshnessLabel(
      meta({
        id: "a",
        fetched_at: "2025-07-01T12:00:00Z",
        updated_at: "2025-08-01T12:00:00Z",
      }),
    );
    expect(last).not.toBe(fetched);
  });

  it("withPendingPricing projects the save onto sources and live meta", () => {
    const next = withPendingPricing(
      {
        cost_tracking_enabled: false,
        sources,
        available_sources: [
          meta({ id: "models-dev", enabled: true, status: "ok" }),
          meta({ id: "litellm" }),
        ],
      },
      {
        cost_tracking_enabled: true,
        sources: selectPricingSource(sources, "litellm"),
      },
    );
    expect(next.cost_tracking_enabled).toBe(true);
    expect(next.sources.find((s) => s.enabled)?.id).toBe("litellm");
    expect(next.available_sources.map((s) => [s.id, s.enabled, s.status])).toEqual([
      ["models-dev", false, "ok"],
      ["litellm", true, "offline"],
    ]);
  });

  it("hasSelectedSource requires exactly one selected source", () => {
    expect(hasSelectedSource(sources)).toBe(true);
    expect(
      hasSelectedSource([
        { id: "models-dev", enabled: false },
        { id: "litellm", enabled: false },
      ]),
    ).toBe(false);
    expect(
      hasSelectedSource([
        { id: "models-dev", enabled: true },
        { id: "litellm", enabled: true },
      ]),
    ).toBe(false);
  });
});
