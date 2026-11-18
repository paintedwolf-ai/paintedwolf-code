import { createUniqueId, For, Show } from "solid-js";
import { cn } from "../../shared/cn.ts";
import { SelectionThumb } from "./SelectionThumb.tsx";

export type BrowseSegmentedOption<Id extends string = string> = {
  id: Id;
  label: string;
  testId?: string;
  ariaLabel?: string;
  disabled?: boolean;
  description?: string;
};

export type BrowseSegmentedProps<Id extends string = string> = {
  options: readonly BrowseSegmentedOption<Id>[];
  value: Id;
  onChange: (id: Id) => void;
  onReselect?: (id: Id) => void;
  ariaLabel: string;
  class?: string;
  testId?: string;
  disabled?: boolean;
  semantics?: "toggle" | "radio";
  busy?: boolean;
  keepFocus?: boolean;
  ref?: (element: HTMLDivElement) => void;
  onSelectionSettled?: (id: Id) => void;
};

export function BrowseSegmented<Id extends string>(props: BrowseSegmentedProps<Id>) {
  let track: HTMLDivElement | undefined;
  const radio = () => props.semantics === "radio";
  const disabled = (option: BrowseSegmentedOption<Id>) => Boolean(props.disabled || option.disabled);
  const activate = (option: BrowseSegmentedOption<Id>) => {
    if (disabled(option)) return;
    if (props.value === option.id) props.onReselect?.(option.id);
    else props.onChange(option.id);
  };

  const navigate = (event: KeyboardEvent, current: BrowseSegmentedOption<Id>) => {
    if (!radio()) return;
    const back = event.key === "ArrowLeft" || event.key === "ArrowUp";
    const forward = event.key === "ArrowRight" || event.key === "ArrowDown";
    if (!back && !forward && event.key !== "Home" && event.key !== "End") return;
    event.preventDefault();

    const available = props.options.filter((option) => !disabled(option));
    if (available.length === 0) return;
    const index = available.indexOf(current);
    const target = event.key === "Home" ? available[0]
      : event.key === "End" ? available.at(-1)
      : available[index < 0 && back ? available.length - 1
        : (index + (forward ? 1 : -1) + available.length) % available.length];
    if (!target) return;

    Array.from(track?.querySelectorAll("button") ?? [])
      .find((button) => button.dataset.value === target.id)?.focus();
    if (target.id !== props.value) props.onChange(target.id);
  };

  return (
    <div
      ref={(element) => {
        track = element;
        props.ref?.(element);
      }}
      class={cn("den-browse-segmented", props.class)}
      role={radio() ? "radiogroup" : "group"}
      aria-label={props.ariaLabel}
      aria-busy={props.busy || undefined}
      data-testid={props.testId}
    >
      <SelectionThumb active={props.value} onSelectionSettled={() => props.onSelectionSettled?.(props.value)} />
      <For each={props.options}>
        {(opt) => {
          const selected = () => props.value === opt.id;
          const descriptionId = createUniqueId();
          return (
            <>
              <button
                type="button"
                class="den-browse-segment"
                classList={{ "den-browse-segment--on": selected() }}
                data-testid={opt.testId}
                data-value={opt.id}
                role={radio() ? "radio" : undefined}
                aria-label={opt.ariaLabel ?? opt.label}
                aria-pressed={!radio() ? selected() : undefined}
                aria-checked={radio() ? selected() : undefined}
                aria-disabled={disabled(opt) || undefined}
                aria-describedby={opt.description ? descriptionId : undefined}
                tabIndex={radio() ? (selected() ? 0 : -1) : undefined}
                disabled={!radio() && props.disabled}
                onPointerDown={(event) => {
                  if (props.keepFocus) event.preventDefault();
                }}
                onClick={() => activate(opt)}
                onKeyDown={(event) => navigate(event, opt)}
              >
                <span class="den-browse-segment__label" data-label={opt.label}>
                  {opt.label}
                </span>
              </button>
              <Show when={opt.description}>
                <span id={descriptionId} class="sr-only">{opt.description}</span>
              </Show>
            </>
          );
        }}
      </For>
    </div>
  );
}
