import { createEffect, createMemo, on, type Accessor } from "solid-js";
import { createPreparation, createPresentationWaiting } from "../../ui/presentation.ts";
import { useSurfaceReveal } from "../../ui/surface-reveal.ts";

/** The editor and walk chrome share one preparation and publication boundary. */
export function createWalkPresentation(options: {
  active: Accessor<boolean>;
  loading: Accessor<boolean>;
  ready?: Accessor<boolean>;
}) {
  const preparation = createPreparation();
  const reveal = useSurfaceReveal({
    name: "walk",
    participate: false,
    deferInitialReveal: true,
    ready: () => options.active() && !options.loading() && (options.ready?.() ?? true) && preparation.ready(),
  });
  const unprepared = createMemo(() => !options.active() || options.loading());
  createEffect(on(unprepared, (value) => { if (value) reveal.reset(); }, { defer: true }));
  const waiting = createPresentationWaiting(() => options.active() && !reveal.ready());
  return { preparation, reveal, waiting };
}
