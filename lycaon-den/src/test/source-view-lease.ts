/** A host lease that outlasts any test from now, so no attachment finds it lapsed. */
export function heldLeaseExpiry(): string {
  return new Date(Date.now() + 24 * 3_600_000).toISOString();
}
