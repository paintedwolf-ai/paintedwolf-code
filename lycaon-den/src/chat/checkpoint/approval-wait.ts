/** Pending cards show elapsed time from one minute onward. */
export function approvalWaitLabel(issuedAt: string, nowMs: number): string {
  const issued = Date.parse(issuedAt);
  if (!Number.isFinite(issued)) return "";
  const elapsedMs = nowMs - issued;
  if (elapsedMs < 60_000) return "";
  const minutes = Math.floor(elapsedMs / 60_000);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} h`;
  return `${Math.floor(hours / 24)} d`;
}
