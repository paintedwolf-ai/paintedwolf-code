import { createEffect, on, onCleanup } from "solid-js";
import { subscribeSourceInvalidation } from "../files/source/source-invalidation.ts";
import { createBoundedDebouncedAsyncScheduler } from "../store/coalesced-async.ts";

/** Search may span projects. Subscribe while active and revalidate on return. */
export function createSourceSearchRefresh(
  active: () => boolean,
  refresh: () => Promise<void>,
): void {
  let initialized = false;
  createEffect(on(active, (live) => {
    const returning = initialized;
    initialized = true;
    if (!live) return;
    const scheduler = createBoundedDebouncedAsyncScheduler(async () => {
      if (active()) await refresh();
    }, 400, 2000);
    const unsubscribe = subscribeSourceInvalidation(scheduler.schedule);
    if (returning) scheduler.schedule();
    onCleanup(() => { unsubscribe(); scheduler.cancel(); });
  }));
}
