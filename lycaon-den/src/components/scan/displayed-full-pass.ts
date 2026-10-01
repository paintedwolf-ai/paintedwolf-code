import { createEffect, createSignal, on, onCleanup } from "solid-js";
import type { SecurityFullPass, SecurityOverview } from "../../api/types.ts";
import type { DisplayedFullPass } from "../../lib/scan-coverage.ts";
import { MIN_VISIBLE_MS, MinVisibleHold } from "../../ui/min-visible-hold.ts";

/** Retains the active full pass for at least MIN_VISIBLE_MS after completion. */
export function createDisplayedFullPass(
  overview: () => SecurityOverview | null,
  scope: () => string | undefined,
  minVisibleMs = MIN_VISIBLE_MS,
): () => DisplayedFullPass | null {
  const [heldId, setHeldId] = createSignal<string>();
  let lastRunning: SecurityFullPass | undefined;
  const hold = new MinVisibleHold<SecurityFullPass>(
    (visible) => setHeldId([...visible.keys()].pop()),
    { same: (shown, next) => shown.assessment_id === next.assessment_id, minVisibleMs },
  );

  createEffect(
    on(scope, () => {
      lastRunning = undefined;
      hold.clear();
    }, { defer: true }),
  );
  createEffect(() => {
    const running = overview()?.running;
    if (running) lastRunning = running;
    hold.update(running ? new Map([[running.assessment_id, running]]) : new Map());
  });
  onCleanup(() => hold.dispose());

  return () => {
    const current = overview();
    if (current?.running) return { pass: current.running, live: true };
    const id = heldId();
    if (!id) return null;
    if (current?.last_full?.assessment_id === id) return { pass: current.last_full, live: false };
    return lastRunning?.assessment_id === id ? { pass: lastRunning, live: false } : null;
  };
}
