/** The tab rail's tabs. Order here is the rail's left-to-right order. */
export type TabId = "progress" | "workflows" | "git" | "workers";

export const TAB_PANEL_TRANSITION_MS = 180;

/** Toggle the clicked tab while keeping panels mutually exclusive. */
export function nextOpen(
  current: TabId | null,
  clicked: TabId,
): TabId | null {
  return current === clicked ? null : clicked;
}

/** Format a chip count within the rail width. */
export function tabBadge(count: number): string | undefined {
  if (count <= 0) return undefined;
  return count > 99 ? "99+" : String(count);
}
