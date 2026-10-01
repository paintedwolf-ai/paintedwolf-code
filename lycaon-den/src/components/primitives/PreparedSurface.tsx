import { Show, type JSX } from "solid-js";
import { createPreparation, createPresentationWaiting } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { useSurfaceReveal, surfaceRevealDom } from "../../ui/surface-reveal.ts";

/** Prepares body content without remounting its controls. */
export function PreparedSurface(props: {
  name: string;
  required?: boolean;
  ready?: () => boolean;
  children: JSX.Element;
  waiting?: JSX.Element;
  class?: string;
}) {
  const preparation = createPreparation();
  const reveal = useSurfaceReveal({
    name: props.name,
    participate: props.required,
    ready: () => (props.ready?.() ?? true) && preparation.ready(),
    deferInitialReveal: true,
    notice: preparation.notice,
  });
  const attrs = () => surfaceRevealDom(reveal);
  const waiting = createPresentationWaiting(() => !reveal.ready());
  return (
    <div class={`den-prepared-surface ${props.class ?? ""}`}>
      <div {...attrs()} data-presentation-pending={preparation.pending().join(",")}>
        <PresentationProvider preparation={preparation}>{props.children}</PresentationProvider>
      </div>
      <Show when={waiting()}>
        <div class="den-presentation-wait" role="status">
          {props.waiting ?? preparation.notice()?.message ?? "Loading…"}
        </div>
      </Show>
    </div>
  );
}
