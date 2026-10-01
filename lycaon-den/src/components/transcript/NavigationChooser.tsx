import { For, Show, createSignal, onMount, onCleanup } from "solid-js";
import type { NavigationReference, NavigationTarget } from "../../api/types.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { focusRetainingSelection } from "../../platform/interaction/selection-lease.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

const statusCopy: Record<NavigationReference["status"], string> = {
  pending: "Looking for this file…",
  ambiguous: "Choose the referenced file.",
  missing: "No file is available at this referenced location.",
  unavailable: "This location is unavailable in the current workspace.",
  resolved: "Open this file.",
};

export function NavigationChooser(props: {
  action?: "open" | "reveal";
  anchor: Element;
  reference?: NavigationReference;
  error?: string;
  busy: boolean;
  roots?: readonly ResolveProjectRoot[];
  onDismiss: () => void;
  onRetry: () => void;
  onOpen: (target: NavigationTarget) => void;
}) {
  let surface: HTMLDivElement | undefined;
  onMount(() => { if (surface) focusRetainingSelection(surface, { preventScroll: true }); });
  onCleanup(() => {
    if (surface?.contains(document.activeElement) && props.anchor instanceof HTMLElement && props.anchor.isConnected) focusRetainingSelection(props.anchor, { preventScroll: true });
  });
  const [filter, setFilter] = createSignal("");
  const candidates = () => (props.reference?.candidates ?? []).filter((target) => target.path.toLocaleLowerCase().includes(filter().toLocaleLowerCase()));
  const label = (target: NavigationTarget) => {
    const root = props.roots?.find((item) => item.id === target.root_id);
    return `${root?.label ? `@${root.label}/` : `${target.root_id}/`}${target.path}${target.worker_id ? ` (worker ${target.worker_id})` : ""}`;
  };
  return <AnchoredSurface ref={(element) => { surface = element; }} tabIndex={-1} chrome anchor={() => props.anchor} role="dialog" ariaLabel={props.action === "reveal" ? "Reveal in tree" : "File navigation"} class="den-menu-surface flex w-[min(32rem,calc(100vw-2rem))] flex-col text-den-body text-den-text" onDismiss={props.onDismiss} dismissOnEscape>
    <p class="m-0 px-den-4 py-den-3 text-den-text-muted" role="status">{props.error ?? (props.busy ? "Looking for this file…" : props.reference ? statusCopy[props.reference.status] : "File lookup is unavailable.")}</p>
    <Show when={(props.reference?.candidates?.length ?? 0) > 5}>
      <div class="px-den-3 pb-den-2">
        <DenInput aria-label="Filter these files" type="search" value={filter()} onInput={(event) => setFilter(event.currentTarget.value)} />
      </div>
    </Show>
    <Show when={(props.reference?.candidates?.length ?? 0) > 0}>
      <Scrollport class="max-h-[20rem] min-h-0" contentClass="flex flex-col p-den-2 [overflow-wrap:anywhere]">
        <For each={candidates()}>{(target) => <button type="button" class="den-context-menu__item" disabled={props.busy} onClick={() => props.onOpen(target)}>{label(target)}</button>}</For>
      </Scrollport>
    </Show>
    <div class="flex flex-wrap justify-end gap-den-3 border-t border-den-border p-den-3">
      <Show when={props.reference?.status !== "ambiguous"}><DenButton variant="secondary" compact disabled={props.busy} onClick={() => props.onRetry()}>Retry lookup</DenButton></Show>
      <DenButton variant="ghost" compact onClick={props.onDismiss}>Close</DenButton>
    </div>
  </AnchoredSurface>;
}
