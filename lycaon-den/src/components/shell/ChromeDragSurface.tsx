import { tauriDragRegionProps, usesCustomWindowChrome } from "../../platform/runtime.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";

type Props = {
  class?: string;
};

/** Drag layer for custom window chrome. */
export function ChromeDragSurface(props: Props) {
  if (!usesCustomWindowChrome()) return null;

  return (
    <div
      class={props.class ?? "den-shell-chrome-drag-surface"}
      aria-hidden="true"
      {...chromeProps()}
      {...tauriDragRegionProps()}
      // Prevent overlay dismissal from drag clicks.
      onClick={(event) => event.stopPropagation()}
    />
  );
}
