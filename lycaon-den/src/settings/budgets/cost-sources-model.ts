import type {
  PricingSourceMeta,
  PricingSourceStatus,
  SettingsPricing,
  SettingsPricingResponse,
  SettingsPricingSource,
} from "../../api/types.ts";

export type StatusBadgeTone = "ok" | "stale" | "error" | "offline";

export type StatusBadge = {
  label: string;
  tone: StatusBadgeTone;
};

/** Select exactly one pricing source. */
export function selectPricingSource(
  sources: readonly SettingsPricingSource[],
  id: string,
): SettingsPricingSource[] {
  return sources.map((source) => ({
    ...source,
    enabled: source.id === id,
  }));
}

/** Fresh rows isolate the pending request from in-place store reconciliation. */
export function withPendingPricing(
  current: SettingsPricingResponse,
  next: SettingsPricing,
): SettingsPricingResponse {
  const nextSources = next.sources ?? current.sources;
  const enabled = new Map(nextSources.map((source) => [source.id, source.enabled]));
  return {
    ...current,
    cost_tracking_enabled: next.cost_tracking_enabled ?? current.cost_tracking_enabled,
    sources: nextSources.map((source) => ({ ...source })),
    available_sources: current.available_sources.map((meta) => ({
      ...meta,
      enabled: enabled.get(meta.id) ?? meta.enabled,
    })),
  };
}

const STATUS_BADGES: Record<PricingSourceStatus, StatusBadge> = {
  ok: { label: "OK", tone: "ok" },
  stale: { label: "Stale", tone: "stale" },
  error: { label: "Error", tone: "error" },
  offline: { label: "Offline", tone: "offline" },
};

export function statusBadge(meta: PricingSourceMeta): StatusBadge {
  return STATUS_BADGES[meta.status] ?? STATUS_BADGES.offline;
}

/** Prefer updated_at, else fetched_at, else “never”. */
export function freshnessLabel(meta: PricingSourceMeta): string {
  const raw = meta.updated_at ?? meta.fetched_at;
  if (!raw) return "never";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "never";
  return d.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

/** Main toggle may turn on only when one feed is selected. */
export function hasSelectedSource(
  sources: readonly SettingsPricingSource[],
): boolean {
  return sources.filter((source) => source.enabled).length === 1;
}
