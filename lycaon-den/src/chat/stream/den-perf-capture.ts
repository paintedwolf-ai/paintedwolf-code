/** Sends main-thread stalls and breadcrumbs to enabled debug capture. */

import { lycaonFetch } from "../../api/http.ts";
import type { DenPerfEvent, DenPerfEventsRequest } from "../../api/types.ts";
import { getBackendConnection } from "../../platform/connection/backend.ts";
import { fullDebugLoggingPref } from "../../settings/system/debug-prefs.ts";

const MAX_EVENTS_PER_BATCH = 32;

export function postDenPerfEvents(events: readonly DenPerfEvent[]): void {
  if (!fullDebugLoggingPref() || events.length === 0) return;
  const connection = getBackendConnection();
  if (!connection?.apiToken?.trim()) return;

  const observedAt = new Date().toISOString();
  for (let offset = 0; offset < events.length; offset += MAX_EVENTS_PER_BATCH) {
    const body: DenPerfEventsRequest = {
      events: events.slice(offset, offset + MAX_EVENTS_PER_BATCH).map((event): DenPerfEvent => ({
        observed_at: event.observed_at ?? observedAt,
        channel: event.channel ?? "perf",
        event: event.event,
        detail: event.detail,
      })),
    };
    try {
      void lycaonFetch(connection, "/v1/debug/den-perf", {
        method: "POST",
        body: JSON.stringify(body),
      }).catch(() => {
        /* Debug capture is best effort. */
      });
    } catch {
      /* The connection changed before dispatch. */
    }
  }
}
