/** Den's entry fade; reduced motion collapses it globally. */
export const SUMMARY_FADE_CLASS = "den-enter-fade";

const FADE_LIMIT = 256;

/** Summary sections already shown, so remounts and reopens skip the fade. */
const shown = new Set<string>();

function normalize(key: string | undefined): string {
  return key?.trim() ?? "";
}

function remember(key: string): void {
  if (shown.size >= FADE_LIMIT) shown.clear();
  shown.add(key);
}

/** True the first time a section is shown, which is when it fades in. */
export function claimSummaryFade(key: string | undefined): boolean {
  const normalized = normalize(key);
  if (!normalized || shown.has(normalized)) return false;
  remember(normalized);
  return true;
}

/** Records a section shown without arriving, such as text settled on open. */
export function markSummaryShown(key: string | undefined): void {
  const normalized = normalize(key);
  if (normalized) remember(normalized);
}

export function resetSummaryFadesForTests(): void {
  shown.clear();
}
