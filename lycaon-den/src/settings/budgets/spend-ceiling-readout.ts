import type { CostSummary } from "../../api/types.ts";
import { estimateView, formatUSD, joinReasons, priced } from "../../cost/cost-format.ts";

export type SpendCeilingReadout =
  | { kind: "off" }
  | { kind: "unpriced"; label: string }
  | { kind: "priced"; label: string; ratio: number; warningRatio?: number }
  | { kind: "idle"; label: string };

/** Spend-ceiling warning threshold. */
export const SPEND_CEILING_WARN_RATIO = 0.8;

/** True while the session is near its ceiling but has not reached it. */
export function spendCeilingApproaching(readout: SpendCeilingReadout): boolean {
  return (
    readout.kind === "priced" &&
    readout.ratio >= (readout.warningRatio ?? SPEND_CEILING_WARN_RATIO) &&
    readout.ratio < 1
  );
}

/** Pure view-model for the Budgets spend-ceiling live readout. */
export function spendCeilingReadout(input: {
  enabled: boolean;
  ceilingUsd: number;
  warningRatio?: number;
  summary: CostSummary | undefined;
}): SpendCeilingReadout {
  if (!input.enabled) {
    return { kind: "off" };
  }
  const summary = input.summary;
  if (!summary) {
    return { kind: "idle", label: "Waiting for session spend…" };
  }
  if (!priced(summary)) {
    return {
      kind: "unpriced",
      label: "not enforced (pricing unavailable)",
    };
  }
  const spent = (summary.estimated_nano_usd ?? 0) / 1e9;
  const ceiling = input.ceilingUsd > 0 ? input.ceilingUsd : 0;
  const ratio = ceiling > 0 ? Math.min(1, Math.max(0, spent / ceiling)) : 0;
  const view = estimateView(summary);
  return {
    kind: "priced",
    label:
      view.coverage === "lower_bound"
        ? `spent ${view.phrase} of ${formatUSD(ceiling)} (${joinReasons(view.reasons)}; actual charges may exceed this limit)`
        : `spent ${view.phrase} of ${formatUSD(ceiling)}`,
    ratio,
    warningRatio: input.warningRatio,
  };
}

/** Next ceiling after Raise & resume — at least spent+$1, prefer 2× current. */
export function raisedSpendCeilingUsd(current: number, spent: number): number {
  const safeCurrent = Number.isFinite(current) && current > 0 ? current : 0;
  const safeSpent = Number.isFinite(spent) && spent > 0 ? spent : 0;
  const doubled = safeCurrent > 0 ? safeCurrent * 2 : Math.max(5, safeSpent + 1);
  const floor = Math.max(safeSpent + 1, safeCurrent + 1);
  return Math.round(Math.max(floor, doubled) * 100) / 100;
}

export const SPEND_CEILING_NOTICE_CODE = "session_spend_ceiling_reached";
