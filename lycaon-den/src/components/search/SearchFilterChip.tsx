import { Show, createSignal } from "solid-js";
import type { SearchFacet } from "../../api/types.ts";
import { DenMenuTrigger } from "../primitives/DenMenuTrigger.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { SearchFilterMenu } from "./SearchFilterMenu.tsx";
import { hasSearchRefinements } from "../../search/search-filter-model.ts";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";

type Props = {
  facets: SearchFacet[];
  query: string;
  onQueryChange: (query: string) => void;
  include: string;
  exclude: string;
  onIncludeChange: (value: string) => void;
  onExcludeChange: (value: string) => void;
};

export function SearchFilterChip(props: Props) {
  const [open, setOpen] = createSignal(false);
  let triggerEl: HTMLButtonElement | undefined;
  let panelEl: HTMLDivElement | undefined;
  createAnchoredPopoverFocus(
    open,
    () => panelEl,
    {
      trigger: () => triggerEl,
      onEscape: () => setOpen(false),
    },
  );
  const active = () =>
    hasSearchRefinements(props.query, props.include, props.exclude);

  return (
    <>
      <DenMenuTrigger
        label="Filter"
        open={open()}
        popup="dialog"
        active={active()}
        testId="search-filter"
        buttonRef={(element) => {
          triggerEl = element;
        }}
        onClick={() => setOpen((v) => !v)}
      />
      <Show when={open()}>
        <AnchoredSurface
          ref={(element) => {
            panelEl = element;
          }}
          class="den-menu-surface den-search-filter__panel"
          role="dialog"
          ariaLabel="Search filters"
          anchor={() => triggerEl}
          preferredSide="bottom"
          align="start"
          onDismiss={() => setOpen(false)}
        >
          <SearchFilterMenu
            facets={props.facets}
            query={props.query}
            onQueryChange={props.onQueryChange}
            include={props.include}
            exclude={props.exclude}
            onIncludeChange={props.onIncludeChange}
            onExcludeChange={props.onExcludeChange}
          />
        </AnchoredSurface>
      </Show>
    </>
  );
}
