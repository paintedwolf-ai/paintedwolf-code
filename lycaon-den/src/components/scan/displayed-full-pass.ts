import { createEffect, createSignal, on, onCleanup } from "solid-js";
import type { SecurityFullPass, SecurityOverview } from "../../api/types.ts";
import type { DisplayedFullPass } from "../../lib/scan-coverage.ts";
import { Linger, type Lingered } from "../../ui/linger.ts";

/** How long a finished pass stays shown as finished. */
export const FINISHED_PASS_LINGER_MS = 1_600;

/** The running pass, then briefly the pass it became. */
export function createDisplayedFullPass(
  overview: () => SecurityOverview | null,
  scope: () => string | undefined,
  lingerMs = FINISHED_PASS_LINGER_MS,
): () => DisplayedFullPass | null {
  const [shown, setShown] = createSignal<Lingered<SecurityFullPass> | undefined>();
  const linger = new Linger<SecurityFullPass>(
    (passes) => setShown([...passes.values()].pop()),
    { lingerMs, same: (current, next) => current.assessment_id === next.assessment_id },
  );

  createEffect(on(scope, () => linger.clear(), { defer: true }));
  createEffect(() => {
    const running = overview()?.running;
    linger.update(running ? new Map([[running.assessment_id, running]]) : new Map());
  });
  onCleanup(() => linger.dispose());

  return () => {
    const current = overview();
    if (current?.running) return { pass: current.running, live: true };
    const finished = shown();
    if (!finished) return null;
    const id = finished.value.assessment_id;
    return { pass: current?.last_full?.assessment_id === id ? current.last_full : finished.value, live: false };
  };
}
