import type { CostSummary, TokenTotals } from "../api/types.ts";
import { formatNanoUsd, NANO_PER_USD } from "./nano-usd.ts";

export type CostEstimateCoverage = CostSummary["estimate_coverage"];

/** Formats estimated USD without hiding sub-cent spend. */
export function formatUSD(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return "—";
  const cents = new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
  if (n > 0 && n < 0.005) return `<${cents.format(0.01)}`;
  return cents.format(n);
}

/** Formats per-1k rates as compact per-million rates. */
export function formatModelTokenRates(
  inputPer1k: number,
  outputPer1k: number,
): string {
  return `($${formatRatePerMillion(inputPer1k)}/$${formatRatePerMillion(outputPer1k)} per 1M)`;
}

/** Converts per-1k USD to a trimmed per-million amount. */
function formatRatePerMillion(per1k: number): string {
  if (!Number.isFinite(per1k) || per1k < 0) return "—";
  const perM = per1k * 1000;
  if (perM === 0) return "0";
  const abs = Math.abs(perM);
  if (abs >= 100) return perM.toFixed(0);
  if (abs >= 0.01) return perM.toFixed(2).replace(/\.00$/, "");
  return perM.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
}

export function formatTokens(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return "—";
  return Math.round(n).toLocaleString();
}

/** Formats a compact approximate token count. */
export function formatTokensCompact(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return "—";
  const value = Math.round(n);
  if (value < 1000) return String(value);
  const [scaled, suffix] =
    value < 999_500
      ? [value / 1000, "K"]
      : value < 999_500_000
        ? [value / 1_000_000, "M"]
        : [value / 1_000_000_000, "B"];
  const digits = scaled < 10 ? 1 : 0;
  return `~${scaled.toFixed(digits).replace(/\.0$/, "")}${suffix}`;
}

/** Formats a compact prompt/completion split. */
export function formatTokenSplit(totals: TokenTotals): string {
  return `${formatTokensCompact(totals.prompt)} in · ${formatTokensCompact(totals.completion)} out`;
}

/** Formats an exact prompt/completion split. */
export function formatTokenSplitExact(totals: TokenTotals): string {
  const base = `${formatTokens(totals.prompt)} in · ${formatTokens(totals.completion)} out`;
  if (!totals.cache_read && !totals.cache_write) return base;
  return `${base} · ${formatTokens(totals.cache_read ?? 0)} cache read · ${formatTokens(totals.cache_write ?? 0)} cache written`;
}

/** Formats pricing provenance. */
export function sourceAsOfLabel(summary: CostSummary | undefined): string {
  if (!priced(summary)) return "pricing unavailable";
  const provenance = summary.pricing_provenance;
  if (provenance.length === 0) return "unknown source";
  return provenance
    .map((entry) => {
      const source = entry.source.trim() || "unknown source";
      if (!entry.priced_at) return source;
      const d = new Date(entry.priced_at);
      if (Number.isNaN(d.getTime())) return source;
      return `${source} · as of ${d.toLocaleString(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      })}`;
    })
    .join("; ");
}

/** Reports whether any of the summary's usage carries a price. */
export function priced(summary: CostSummary | undefined): summary is CostSummary {
  return summary != null && summary.estimate_coverage !== "unpriced";
}

/** Returns unreported calls that may incur charges. */
export function unreportedChargedCalls(summary: CostSummary | undefined): number {
  return summary?.unknown_charged_calls ?? 0;
}

/** Returns unreported no-charge calls. */
export function unreportedFreeCalls(summary: CostSummary | undefined): number {
  return Math.max(
    0,
    (summary?.unknown_calls ?? 0) - unreportedChargedCalls(summary),
  );
}

export function hostMeasuredTokens(summary: CostSummary | undefined): number {
  return summary?.host_measured_tokens ?? 0;
}

export function callCount(n: number, noun: string): string {
  return `${n} ${noun} call${n === 1 ? "" : "s"} with incomplete usage`;
}

/**
 * One estimate as separate facts. The amount is only ever a rounded figure;
 * what it covers travels beside it, never prefixed onto it, so a sub-cent
 * lower bound stays representable.
 */
export type CostEstimateView = {
  coverage: CostEstimateCoverage;
  /** Rounded USD over the priced usage; absent when nothing is priced. */
  amount?: string;
  /** Sentence-case label for a coverage that qualifies the amount. */
  qualifier?: string;
  /** Prose form for a sentence: "$2.50", "at least $2.50", "less than $0.01 in priced usage". */
  phrase: string;
  /** Why the coverage is a lower bound, in display order. */
  reasons: string[];
};

/** Builds the estimate view the host's coverage stamp implies. */
export function estimateView(summary: CostSummary | undefined): CostEstimateView {
  if (!priced(summary)) {
    return {
      coverage: "unpriced",
      qualifier: "Pricing unavailable",
      phrase: "unpriced",
      reasons: [],
    };
  }
  const nano = summary.estimated_nano_usd ?? 0;
  const amount = formatNanoUsd(nano);
  if (summary.estimate_coverage === "complete") {
    return { coverage: "complete", amount, phrase: amount, reasons: [] };
  }
  const reasons: string[] = [];
  if ((summary.unpriced_tokens ?? 0) > 0) {
    reasons.push(`${formatTokensCompact(summary.unpriced_tokens)} tokens unpriced`);
  }
  const charged = unreportedChargedCalls(summary);
  if (charged > 0) reasons.push(callCount(charged, "provider"));
  // Below display precision the figure says nothing; the sentence says what is known.
  const phrase = nano < NANO_PER_USD / 200 ? "less than $0.01 in priced usage" : `at least ${amount}`;
  return { coverage: "lower_bound", amount, qualifier: "Lower bound", phrase, reasons };
}

/** Joins lower-bound reasons for prose. */
export function joinReasons(reasons: string[]): string {
  if (reasons.length <= 1) return reasons.join("");
  return `${reasons.slice(0, -1).join(", ")} and ${reasons[reasons.length - 1]}`;
}

export function trackingSinceLabel(since: string | null | undefined): string {
  if (!since) return "";
  const d = new Date(since);
  if (Number.isNaN(d.getTime())) return `Tracking since ${since}`;
  return `Tracking since ${d.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  })}`;
}

/** Formats a spend ceiling. */
export function formatCeilingUSD(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "—";
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: Number.isInteger(n) ? 0 : 2,
    maximumFractionDigits: 2,
  }).format(n);
}

/** Cost chip label, coverage, and ceiling proximity. */
export type CostChipView = {
  label: string;
  coverage: Exclude<CostEstimateCoverage, "unpriced">;
  /** Present for an active priced ceiling. */
  ceiling?: { ratio: number };
};

/** Builds the session cost chip view; undefined when nothing is priced. */
export function costChipView(
  summary: CostSummary | undefined,
  boardSummary: CostSummary | null | undefined,
  ceiling?: { enabled: boolean; ceilingUsd: number },
): CostChipView | undefined {
  summary = summary ?? boardSummary ?? undefined;
  const view = estimateView(summary);
  if (view.coverage === "unpriced" || view.amount == null) return undefined;
  const usd = ceiling?.ceilingUsd ?? 0;
  const armed = ceiling?.enabled === true && usd > 0;
  if (!armed) {
    return { label: view.amount, coverage: view.coverage };
  }
  const spent = summary?.estimated_nano_usd ?? 0;
  const ratio = Math.min(1, Math.max(0, spent / (usd * NANO_PER_USD)));
  return {
    label: `${view.amount} / ${formatCeilingUSD(usd)}`,
    coverage: view.coverage,
    ceiling: { ratio },
  };
}
