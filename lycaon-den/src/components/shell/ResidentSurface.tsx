import { Show, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import { DenButton } from "../primitives/DenButton.tsx";
import { afterPaint } from "../../ui/surface-reveal.ts";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { ResidentPresenceProvider, useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { beginScrollMeasureQuiet } from "../../platform/scrolling/themed-scrollbars.ts";
import { createPreparation, createPresentationWaiting, type Preparation } from "../../ui/presentation.ts";
import { PresentationProvider, usePresentationParticipant } from "../../ui/presentation-context.tsx";

type Props = {
  surfaceKey: string;
  generation?: number;
  payloadAvailable?: boolean;
  presence: ResidentPresence;
  retained?: boolean;
  waitingVisible?: boolean;
  onReady?: (key: string, generation?: number) => void;
  onWithdrawn?: (key: string) => void;
  preparation?: Preparation;
  children: JSX.Element;
};

/** A window resize leaves a pinned surface's layout valid; it lays out once on return. */
function pinParkedSize(el: HTMLElement): () => void {
  const { width, height } = el.getBoundingClientRect();
  if (width <= 0 || height <= 0) return () => {};
  el.style.setProperty("--den-parked-width", `${width}px`);
  el.style.setProperty("--den-parked-height", `${height}px`);
  return () => {
    el.style.removeProperty("--den-parked-width");
    el.style.removeProperty("--den-parked-height");
  };
}

export function ResidentSurface(props: Props) {
  const parentPresence = useResidentPresence();
  const presence = createMemo<ResidentPresence>(() => {
    const parent = parentPresence();
    if (parent === "idle" || props.presence === "idle") return "idle";
    return parent === "pending" || props.presence === "pending" ? "pending" : "active";
  });
  const [root, setRoot] = createSignal<HTMLDivElement | undefined>();
  const preparation = createPreparation();
  const [publishedGeneration, setPublishedGeneration] = createSignal<number>();
  const published = () => publishedGeneration() === (props.generation ?? 0);
  const retained = () => props.retained === true && presence() === "active";
  const waiting = createPresentationWaiting(
    () => (!published() && presence() !== "idle") || retained(),
  );
  const retainedDimmed = () => retained() && waiting() && props.waitingVisible !== false;

  const settled = createMemo(
    () => presence() === "idle" || published() || (props.payloadAvailable !== false && preparation.ready()),
  );
  const canPublish = createMemo(
    () => presence() !== "idle" && props.payloadAvailable !== false && preparation.ready(),
  );
  usePresentationParticipant(props.surfaceKey, settled, preparation.notice);

  createEffect(() => {
    const parent = props.preparation;
    if (parent) {
      onCleanup(
        parent.register(props.surfaceKey, settled, preparation.notice),
      );
    }
  });

  createEffect(() => {
    const generation = props.generation ?? 0;
    if (published() || !canPublish()) return;
    onCleanup(afterPaint(() => {
      if (!canPublish() || generation !== (props.generation ?? 0)) return;
      setPublishedGeneration(generation);
      props.onReady?.(props.surfaceKey, generation);
    }));
  });

  const idle = () => presence() === "idle";
  const pending = () => presence() === "pending";

  // Idle content that no longer describes the host prepares again instead of returning as it was.
  createEffect(() => {
    if (!idle() || !published() || preparation.ready()) return;
    setPublishedGeneration(undefined);
    props.onWithdrawn?.(props.surfaceKey);
  });
  const hidden = () => presence() !== "active" || !published();
  const inaccessible = () => hidden() || retained();

  // Hidden idle surfaces suspend scrollbar measurement and keep their parked size.
  createEffect(() => {
    if (!idle()) return;
    const el = root();
    if (!el) return;
    onCleanup(beginScrollMeasureQuiet(el));
    onCleanup(pinParkedSize(el));
  });

  return (
    <PresentationProvider preparation={preparation}>
    <ResidentPresenceProvider presence={presence()} interactive={!inaccessible()}>
      <div
        ref={setRoot}
        class="den-resident-surface den-retained-presentation"
        classList={{
          "den-resident-surface--idle": idle(),
          "den-resident-surface--pending": pending(),
          "den-resident-surface--active": presence() === "active",
        }}
        data-resident={presence()}
        data-resident-key={props.surfaceKey}
        data-presentation={published() ? "published" : "preparing"}
        data-retained={retainedDimmed() ? "true" : "false"}
        data-presentation-pending={preparation.pending().join(",")}
        data-testid="resident-surface"
        aria-hidden={inaccessible() ? true : undefined}
        inert={inaccessible() ? true : undefined}
      >
        {props.children}
        <Show when={preparation.retryable()}>
          <DenButton variant="link" onClick={preparation.retry}>Retry loading</DenButton>
        </Show>
      </div>
      <Show
        when={
          waiting() &&
          presence() === "active" &&
          props.waitingVisible !== false
        }
      >
        <div class="den-presentation-wait" role="status">
          {retained() ? "Loading…" : preparation.notice()?.message ?? "Loading…"}
        </div>
      </Show>
    </ResidentPresenceProvider>
    </PresentationProvider>
  );
}
