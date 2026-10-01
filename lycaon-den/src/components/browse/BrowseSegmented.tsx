import { For } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { SelectionThumb } from "./SelectionThumb.tsx";

export type BrowseSegmentedOption = {
  id: string;
  label: string;
  testId?: string;
  ariaLabel?: string;
};

export type BrowseSegmentedProps = {
  options: BrowseSegmentedOption[];
  value: string;
  onChange: (id: string) => void;
  ariaLabel: string;
  class?: string;
  testId?: string;
  disabled?: boolean;
};

export function BrowseSegmented(props: BrowseSegmentedProps) {
  return (
    <div
      class={cn("den-browse-segmented", props.class)}
      role="group"
      aria-label={props.ariaLabel}
      data-testid={props.testId}
    >
      <SelectionThumb active={props.value} />
      <For each={props.options}>
        {(opt) => (
          <button
            type="button"
            class="den-browse-segment"
            classList={{ "den-browse-segment--on": props.value === opt.id }}
            data-testid={opt.testId}
            data-value={opt.id}
            aria-label={opt.ariaLabel ?? opt.label}
            aria-pressed={props.value === opt.id}
            disabled={props.disabled}
            onClick={() => {
              if (props.value !== opt.id) props.onChange(opt.id);
            }}
          >
            <span class="den-browse-segment__label" data-label={opt.label}>
              {opt.label}
            </span>
          </button>
        )}
      </For>
    </div>
  );
}
