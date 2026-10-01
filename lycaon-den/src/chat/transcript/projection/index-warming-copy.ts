import type { IndexWarmingMeta } from "../../../api/types.ts";

/** Index warming is visible only in verbose transcripts. */
export function shouldShowIndexWarmingInTranscript(verboseMode?: boolean): boolean {
  return verboseMode ?? false;
}

export const indexWarmingToolName = "Index prefetch";

export function indexWarmingTriggerPhrase(trigger: string): string {
  switch (trigger) {
    case "declared_url":
      return "prefetch for shared link";
    case "search":
      return "after web_search";
    case "fetch":
      return "after fetch_url";
    default:
      return "background prefetch";
  }
}

export function indexWarmingTriggerDetail(trigger: string): string {
  switch (trigger) {
    case "declared_url":
      return "Cached the explicitly shared link and nearby pages on your device.";
    case "search":
      return "Cached hosts from that web_search locally for faster follow-ups.";
    case "fetch":
      return "Cached the opened page's site locally.";
    default:
      return "Background work cached pages into your local web index.";
  }
}

export function indexWarmingTopicNote(topic: string, trigger: string): string {
  const trimmed = topic.trim();
  switch (trigger) {
    case "declared_url":
      return `Site cache for the shared link: "${trimmed}".`;
    case "search":
      return `Follow-up cache for web_search: "${trimmed}".`;
    case "fetch":
      return `Site cache around "${trimmed}".`;
    default:
      return trimmed;
  }
}

export function indexWarmingTierDetail(tier?: string): string | null {
  if (!tier) return null;
  if (tier.includes("seed")) {
    return "Also asked Summarizer for new publisher names to cache.";
  }
  if (tier === "crawl") {
    return "Known and linked hosts only — no Summarizer seed.";
  }
  return null;
}

export function indexWarmingStatsLine(meta: IndexWarmingMeta): string | null {
  const hostN = meta.hosts?.length ?? 0;
  const pageN = meta.pages ?? 0;
  if (hostN === 0 && pageN === 0) return null;
  return `${hostN} host${hostN === 1 ? "" : "s"}, ${pageN} page${pageN === 1 ? "" : "s"}`;
}

/** Chicklet titles omit the topic. */
export function indexWarmingChickletTitle(meta: IndexWarmingMeta): string {
  const parts: string[] = [indexWarmingTriggerPhrase(meta.trigger)];
  const stats = indexWarmingStatsLine(meta);
  if (stats) parts.push(stats);
  if (parts.length === 1 && meta.skip_reason) return meta.skip_reason;
  return parts.join(" · ");
}

export function indexWarmingSkipNote(skipReason: string): string {
  switch (skipReason) {
    case "hourly seed cap":
      return "Summarizer seed skipped — hourly cap reached.";
    case "summarizer unavailable":
      return "Summarizer seed skipped — no model configured.";
    case "interactive search active":
      return "Summarizer seed skipped — a live search was in progress.";
    case "discovery busy":
      return "Skipped — another crawl held the discovery slot.";
    default:
      return `Skipped — ${skipReason}.`;
  }
}
