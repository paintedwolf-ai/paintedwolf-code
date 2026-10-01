import { createSignal, onCleanup, onMount, type JSX } from "solid-js";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";
import { afterPaint } from "../../ui/surface-reveal.ts";

type Props = {
  "data-testid": string;
  "aria-labelledby": string;
  children: JSX.Element;
};

/** Fades the page in after its first paint. */
export function OnboardingStepPage(props: Props) {
  const [open, setOpen] = createSignal(false);

  onMount(() => {
    if (prefersReducedMotion()) {
      setOpen(true);
      return;
    }
    const cancel = afterPaint(() => setOpen(true));
    onCleanup(cancel);
  });

  return (
    <section
      class="onboarding-gate__page"
      classList={{ "onboarding-gate__page--open": open() }}
      data-testid={props["data-testid"]}
      aria-labelledby={props["aria-labelledby"]}
    >
      <div class="onboarding-gate__page-body">{props.children}</div>
    </section>
  );
}
