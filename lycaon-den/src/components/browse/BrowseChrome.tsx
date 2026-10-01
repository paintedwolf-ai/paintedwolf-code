import { Show, type JSX } from "solid-js";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { StageBackChip, type StageBack } from "../shell/StageBackChip.tsx";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { cn } from "../../shared/cn.ts";

export type BrowseChromeProps = {
  back?: StageBack | null;
  title?: JSX.Element;
  primary?: JSX.Element;
  overflow?: JSX.Element;
  trailing?: JSX.Element;
  chips?: JSX.Element;
  /** Chip row metadata. */
  meta?: JSX.Element;
  variant?: "stage" | "embed" | "tabs";
  class?: string;
  testId?: string;
};

export function BrowseChrome(props: BrowseChromeProps) {
  const variant = () => props.variant ?? "stage";
  const hasPrimary = () =>
    props.back != null ||
    props.title != null ||
    props.primary != null ||
    props.overflow != null ||
    props.trailing != null;
  const hasChips = () => props.chips != null || props.meta != null;

  return (
    <Show when={hasPrimary() || hasChips()}>
      <header
        class={cn("den-browse-chrome", props.class)}
        classList={{
          "den-browse-chrome--stage": variant() === "stage",
          "den-browse-chrome--embed": variant() === "embed",
          "den-browse-chrome--tabs": variant() === "tabs",
        }}
        data-testid={props.testId ?? "browse-chrome"}
        // Empty chrome space remains draggable.
        {...chromeProps()}
        {...tauriDragRegionProps()}
      >
        <ChromeDragSurface class="den-browse-chrome__drag-surface" />
        <Show when={hasPrimary()}>
          <div class="den-browse-chrome__primary">
            <Show when={props.back} keyed>
              {(back) => <StageBackChip back={back} />}
            </Show>
            <Show when={props.title != null}>
              <div class="den-browse-chrome__title">{props.title}</div>
            </Show>
            <Show when={props.primary != null}>
              <div class="den-browse-chrome__primary-slot">{props.primary}</div>
            </Show>
            <Show when={props.overflow != null}>
              <div class="den-browse-chrome__overflow">{props.overflow}</div>
            </Show>
            <Show when={props.trailing != null}>
              <div class="den-browse-chrome__trailing">{props.trailing}</div>
            </Show>
          </div>
        </Show>
        <Show when={hasChips()}>
          <div class="den-browse-chrome__chips">
            <Show when={props.chips != null}>
              <div class="den-browse-chrome__chip-slot">{props.chips}</div>
            </Show>
            <Show when={props.meta != null}>
              <div class="den-browse-chrome__meta">{props.meta}</div>
            </Show>
          </div>
        </Show>
      </header>
    </Show>
  );
}
