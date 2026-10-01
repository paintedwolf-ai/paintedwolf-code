import type { WebResearchWarmActivity } from "../../api/types.ts";
import type { DenseReadOnlyListItem } from "../../components/settings/DenseReadOnlyList.tsx";
import { formatRelativeTime } from "../../time/time-copy.ts";

/** Prefer a short page so Local index + activity usually fit one screen. */
export const WEB_RESEARCH_ACTIVITY_PAGE_SIZE = 5;

/** Map warm-activity rows into dense read-only list items (newest first preserved). */
export function webResearchActivityToDenseItems(
  activity: readonly WebResearchWarmActivity[],
  now = Date.now(),
): DenseReadOnlyListItem[] {
  return activity.map((act, index) => {
    const primary = act.tier ? `${act.trigger} · ${act.tier}` : act.trigger;
    const parts: string[] = [];
    if (act.pages != null) parts.push(`${act.pages} pages`);
    if (act.topic) parts.push(act.topic);
    if (act.skip_reason) parts.push(act.skip_reason);
    const atMs = Date.parse(act.at);
    const hasTime = Number.isFinite(atMs);
    return {
      id: `${index}-${act.at}-${act.trigger}`,
      primary,
      secondary: parts.length > 0 ? parts.join(" · ") : undefined,
      meta: hasTime ? formatRelativeTime(atMs, now) : undefined,
      metaTitle: hasTime ? new Date(atMs).toLocaleString() : undefined,
    };
  });
}
