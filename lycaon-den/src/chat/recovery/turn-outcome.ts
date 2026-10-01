import { createSignal } from "solid-js";
import type { SessionEvent, SessionIdleDisposition } from "../../api/types.ts";

const [dispositions, setDispositions] = createSignal<
  Record<string, SessionIdleDisposition>
>({});

/** How the session's last turn ended, or undefined before any idle event. */
export function sessionIdleDisposition(
  sessionId: string | null | undefined,
): SessionIdleDisposition | undefined {
  const id = sessionId?.trim();
  if (!id) return undefined;
  return dispositions()[id];
}

/** A completed turn needs no marker, so it has no outcome row to seat. */
export function hasTurnOutcome(
  disposition: SessionIdleDisposition | undefined,
): boolean {
  return (
    disposition === "user_stopped" ||
    disposition === "turn_error" ||
    disposition === "interrupted"
  );
}

function clearDisposition(sessionId: string): void {
  setDispositions((prev) => {
    if (!(sessionId in prev)) return prev;
    const next = { ...prev };
    delete next[sessionId];
    return next;
  });
}

/** Records the host-declared turn outcome. Busy clears it; an omitted disposition leaves it. */
export function applyTurnOutcome(
  event: Pick<SessionEvent, "id"> &
    Partial<Pick<SessionEvent, "status" | "idle_disposition">>,
): void {
  const sessionId = event.id?.trim();
  if (!sessionId) return;
  if (event.status === "busy") {
    clearDisposition(sessionId);
    return;
  }
  if (!event.idle_disposition) return;
  const next = event.idle_disposition;
  setDispositions((prev) =>
    prev[sessionId] === next ? prev : { ...prev, [sessionId]: next },
  );
}

/** Vitest-only — reset module state between cases. */
export function resetTurnOutcomesForTests(): void {
  setDispositions({});
}
