import { Show, type JSX } from "solid-js";
import { ResidentPortal } from "../../components/primitives/ResidentPortal.tsx";

/** Hosts pinned rows; each slot positions itself within the band. */
export function FilesTreeStickyOverlay(props: {
  host: HTMLElement | undefined;
  /** Covered height above tree content. */
  height: number;
  onKeyDown?: (event: KeyboardEvent) => void;
  /** Passively forwards wheel input to the sibling scroller. */
  onWheel?: (event: WheelEvent) => void;
  children: JSX.Element;
}) {
  return (
    <Show when={props.host} keyed>
      {(host) => (
        <Show when={props.height > 0}>
          <ResidentPortal mount={host}>
            <div
              class="den-files-tree-sticky"
              role="region"
              aria-label="Sticky folders"
              data-testid="files-tree-sticky"
              onKeyDown={props.onKeyDown}
              on:wheel={props.onWheel && { handleEvent: props.onWheel, passive: true }}
              style={{ height: `${Math.ceil(props.height)}px` }}
            >
              {props.children}
            </div>
          </ResidentPortal>
        </Show>
      )}
    </Show>
  );
}
