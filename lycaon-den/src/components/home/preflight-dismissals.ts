/**
 * Per-code dismissal for the Home readiness card, keyed by probe id and code:
 * a new problem from the same probe brings the card back. PreflightNudge keeps
 * blocked probes undismissable.
 */

const dismissed = new Map<string, string>();

/** True when this exact (probe, code) pair was dismissed. */
export function isPreflightDismissed(id: string, code: string): boolean {
  if (!code) return false;
  return dismissed.get(id) === code;
}

export function dismissPreflight(id: string, code: string): void {
  if (!code) return;
  dismissed.set(id, code);
}

/** Test hook. */
export function resetPreflightDismissalsForTests(): void {
  dismissed.clear();
}
