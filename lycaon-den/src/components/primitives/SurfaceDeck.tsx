import { For, createEffect, on, onCleanup, untrack, type JSX } from "solid-js";
import { useResidentStack } from "../../ui/resident-surfaces.ts";
import { ResidentPresenceProvider, useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { ResidentSurface } from "../shell/ResidentSurface.tsx";

/** Prepares incoming tabs while keeping the current tab visible. */
export function SurfaceDeck<K extends string>(props: {
  active: K | null;
  generation?: (key: K) => number;
  payloadAvailable?: (key: K) => boolean;
  retain?: (key: string) => boolean;
  retainOutgoing?: boolean;
  waitingVisible?: boolean;
  interactive?: boolean;
  canPublish?: (key: K) => boolean;
  onDisplayedChange?: (key: K | null) => void;
  /** Reports the incoming key until its prepared surface is displayed. */
  onPublishingChange?: (key: K | null) => void;
  children: (key: K) => JSX.Element;
  class?: string;
}) {
  const presence = useResidentPresence();
  const stack = useResidentStack(() => props.active, {
    generation: (key) => props.generation?.(key as K) ?? 0,
    retain: (key) => props.retain?.(key) ?? true,
    canPublish: (key) => props.canPublish?.(key as K) ?? true,
    get retainOutgoing() { return props.retainOutgoing; },
  });
  createEffect(on(stack.displayed, (key) => props.onDisplayedChange?.(key as K | null)));
  createEffect(() => {
    const key = props.active;
    props.onPublishingChange?.(key && stack.displayed() !== key && stack.presence(key) !== "idle" ? key : null);
  });
  onCleanup(() => {
    props.onDisplayedChange?.(null);
    props.onPublishingChange?.(null);
  });
  return (
    <ResidentPresenceProvider presence={presence()} interactive={props.interactive}>
      <div
        class={`den-surface-deck ${props.class ?? ""}`}
        aria-busy={stack.pending() !== null}
        aria-hidden={props.interactive === false ? true : undefined}
        inert={props.interactive === false ? true : undefined}
      >
        <For each={stack.keys()}>
          {(key) => (
            <ResidentSurface
              surfaceKey={key}
              generation={stack.generation(key)}
              payloadAvailable={props.payloadAvailable?.(key as K)}
              presence={stack.presence(key)}
              retained={
                stack.pending() !== null && stack.presence(key) === "active"
              }
              onReady={stack.markReady}
              onWithdrawn={stack.withdraw}
              waitingVisible={props.waitingVisible}
            >
              {untrack(() => props.children(key as K))}
            </ResidentSurface>
          )}
        </For>
      </div>
    </ResidentPresenceProvider>
  );
}
