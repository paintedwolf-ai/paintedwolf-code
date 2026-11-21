import type { Message } from "../../../api/types.ts";

/**
 * Newest revisions seen over SSE for rows that were not resident when they arrived. A host page
 * fetched before such a patch carries the older revision; reconciling the page restores the newer one.
 */
export class TranscriptObservedRevisions {
  private readonly bySession = new Map<string, Map<string, Message>>();

  note(sessionId: string | undefined, row: Message): void {
    const sid = sessionId?.trim();
    if (!sid) return;
    const rows = this.bySession.get(sid) ?? new Map<string, Message>();
    const prior = rows.get(row.id);
    if (!prior || (prior.seq ?? 0) < (row.seq ?? 0)) rows.set(row.id, row);
    this.bySession.set(sid, rows);
  }

  /** Page rows with any newer observed revision; revisions at or below the page watermark retire. */
  reconcile(sessionId: string, rows: Message[], watermark: number): Message[] {
    const sid = sessionId.trim();
    const observed = this.bySession.get(sid);
    if (!observed) return rows;
    const reconciled = rows.map((row) => {
      const newer = observed.get(row.id);
      return newer && (newer.seq ?? 0) > (row.seq ?? 0) ? newer : row;
    });
    for (const [id, row] of observed) {
      if ((row.seq ?? 0) <= watermark) observed.delete(id);
    }
    if (observed.size === 0) this.bySession.delete(sid);
    return reconciled;
  }
}
