import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
  onCleanup,
} from "solid-js";
import { cn } from "../../shared/cn.ts";
import { AnchoredSurface } from "./AnchoredSurface.tsx";
import { DEN_SCROLLPORT_AXIS_ATTR, DEN_SCROLLPORT_CLASS, DEN_SCROLLPORT_CONTENT_CLASS, DEN_SCROLLPORT_VIEWPORT_CLASS } from "../../platform/scrolling/themed-scrollbars.ts";

type DenSelectOption = {
  value: string;
  label: string;
  description?: string;
  group?: string;
  disabled?: boolean;
};

type Props = {
  options: readonly DenSelectOption[];
  value?: string;
  placeholder?: string;
  class?: string;
  onValueChange?: (value: string) => void;
  "aria-label": string;
  "aria-describedby"?: string;
  "data-testid"?: string;
  id?: string;
  disabled?: boolean;
};

const TYPEAHEAD_RESET_MS = 650;

function nextEnabled(
  options: readonly DenSelectOption[],
  from: number,
  direction: 1 | -1,
): number {
  if (options.length === 0) return -1;
  for (let offset = 1; offset <= options.length; offset += 1) {
    const index = (from + direction * offset + options.length) % options.length;
    if (!options[index]?.disabled) return index;
  }
  return -1;
}

function edgeEnabled(
  options: readonly DenSelectOption[],
  direction: 1 | -1,
): number {
  const start = direction === 1 ? 0 : options.length - 1;
  for (let index = start; index >= 0 && index < options.length; index += direction) {
    if (!options[index]?.disabled) return index;
  }
  return -1;
}

/** Single-choice listbox with full keyboard navigation. */
export function DenSelect(props: Props) {
  const generatedId = createUniqueId();
  const listboxId = () => `${props.id ?? `den-select-${generatedId}`}-listbox`;
  const optionId = (index: number) => `${listboxId()}-option-${index}`;
  const [open, setOpen] = createSignal(false);
  const [active, setActive] = createSignal(-1);
  let triggerEl: HTMLButtonElement | undefined;
  let listboxEl: HTMLDivElement | undefined;
  let listboxViewportEl: HTMLDivElement | undefined;
  let typeahead = "";
  let typeaheadTimer: ReturnType<typeof setTimeout> | undefined;

  // Memoized props keep event-handler reads within the component lifetime.
  const options = createMemo(() => props.options);
  const currentValue = createMemo(() => props.value ?? "");
  const disabled = createMemo(() => props.disabled ?? false);
  const selectedIndex = createMemo(() =>
    options().findIndex((option) => option.value === currentValue()),
  );
  const selected = () => options()[selectedIndex()];

  const focusActive = () => {
    queueMicrotask(() => {
      // Focus preserves ancestor scroll positions.
      listboxEl?.focus({ preventScroll: true });
      const index = active();
      const viewport = listboxViewportEl;
      if (index < 0 || !viewport) return;
      const row = viewport.querySelector<HTMLElement>(
        `[data-option-index="${index}"]`,
      );
      if (!row) return;
      const view = viewport.getBoundingClientRect();
      const target = row.getBoundingClientRect();
      if (target.top < view.top) {
        viewport.scrollTop -= view.top - target.top;
      } else if (target.bottom > view.bottom) {
        viewport.scrollTop += target.bottom - view.bottom;
      }
    });
  };

  const show = (seed?: number) => {
    if (disabled() || options().length === 0) return;
    const selectedOption = selectedIndex();
    const initial =
      seed !== undefined && seed >= 0
        ? seed
        : selectedOption >= 0 && !options()[selectedOption]?.disabled
          ? selectedOption
          : edgeEnabled(options(), 1);
    setActive(initial);
    setOpen(true);
  };

  const close = (refocus = true) => {
    setOpen(false);
    if (refocus) queueMicrotask(() => triggerEl?.focus());
  };

  const pick = (index: number) => {
    const option = options()[index];
    if (disabled() || !option || option.disabled) return;
    close();
    if (option.value !== currentValue()) {
      props.onValueChange?.(option.value);
    }
  };

  const move = (direction: 1 | -1) => {
    const current = active();
    const seed = current >= 0 ? current : direction === 1 ? -1 : 0;
    const next = nextEnabled(options(), seed, direction);
    if (next >= 0) {
      setActive(next);
      focusActive();
    }
  };

  const findTypeahead = (key: string) => {
    typeahead += key.toLocaleLowerCase();
    if (typeaheadTimer) clearTimeout(typeaheadTimer);
    typeaheadTimer = setTimeout(() => {
      typeahead = "";
      typeaheadTimer = undefined;
    }, TYPEAHEAD_RESET_MS);
    const start = Math.max(active(), selectedIndex(), -1);
    for (let offset = 1; offset <= options().length; offset += 1) {
      const index = (start + offset) % options().length;
      const option = options()[index];
      if (
        option !== undefined &&
        !option.disabled &&
        option.label.toLocaleLowerCase().startsWith(typeahead)
      ) {
        setActive(index);
        if (!open()) show(index);
        else focusActive();
        return;
      }
    }
  };

  const focusAdjacent = (backward: boolean) => {
    if (!triggerEl) return;
    const focusable = Array.from(
      document.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ),
    ).filter(
      (element) =>
        !listboxEl?.contains(element) && element.getClientRects().length > 0,
    );
    const index = focusable.indexOf(triggerEl);
    focusable[index + (backward ? -1 : 1)]?.focus();
  };

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape" && open()) {
      event.preventDefault();
      event.stopPropagation();
      close();
      return;
    }
    if (event.key === "Tab" && open()) {
      event.preventDefault();
      const backward = event.shiftKey;
      close(false);
      queueMicrotask(() => focusAdjacent(backward));
      return;
    }
    if (disabled()) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!open()) show();
      else move(event.key === "ArrowDown" ? 1 : -1);
      return;
    }
    if (event.key === "Home" || event.key === "End") {
      if (!open()) return;
      event.preventDefault();
      const next = edgeEnabled(options(), event.key === "Home" ? 1 : -1);
      if (next >= 0) {
        setActive(next);
        focusActive();
      }
      return;
    }
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (open()) pick(active());
      else show();
      return;
    }
    if (
      !event.metaKey &&
      !event.ctrlKey &&
      !event.altKey &&
      event.key.length === 1
    ) {
      findTypeahead(event.key);
    }
  };

  createEffect(() => {
    if (!open()) return;
    queueMicrotask(() => {
      focusActive();
    });
  });

  onCleanup(() => {
    if (typeaheadTimer) clearTimeout(typeaheadTimer);
  });

  return (
    <div
      class={cn("den-select", props.class)}
      data-open={open() ? "true" : undefined}
      data-disabled={disabled() ? "true" : undefined}
    >
      <button
        ref={(element) => {
          triggerEl = element;
        }}
        id={props.id}
        type="button"
        class="den-select__trigger"
        disabled={disabled()}
        data-testid={props["data-testid"]}
        aria-label={props["aria-label"]}
        aria-describedby={props["aria-describedby"]}
        aria-haspopup="listbox"
        aria-expanded={open()}
        aria-controls={open() ? listboxId() : undefined}
        onClick={() => (open() ? close() : show())}
        onKeyDown={onKeyDown}
      >
        <span class="den-select__value">
          {selected()?.label ?? props.placeholder ?? "Select…"}
        </span>
        <span class="den-select__caret" aria-hidden="true" />
      </button>
      <Show when={open()}>
        <AnchoredSurface
          ref={(element) => {
            // The placed surface is the scrollport frame.
            element.setAttribute(DEN_SCROLLPORT_AXIS_ATTR, "y");
            listboxEl = element;
          }}
          anchor={() => triggerEl}
          preferredSide="bottom"
          align="start"
          width="min-anchor"
          overflow="hidden"
          onDismiss={() => close(false)}
          id={listboxId()}
          class={`${DEN_SCROLLPORT_CLASS} den-menu-surface den-select__listbox`}
          role="listbox"
          tabIndex={0}
          ariaLabel={props["aria-label"]}
          ariaActiveDescendant={active() >= 0 ? optionId(active()) : undefined}
          onKeyDown={onKeyDown}
        >
          <div ref={(element) => { listboxViewportEl = element; }} class={DEN_SCROLLPORT_VIEWPORT_CLASS}>
          <div class={`${DEN_SCROLLPORT_CONTENT_CLASS} den-select__listbox-content`}>
            <For each={options()}>
              {(option, index) => {
                const startsGroup = () =>
                  Boolean(option.group) &&
                  (index() === 0 ||
                    options()[index() - 1]?.group !== option.group);
                return (
                  <>
                    <Show when={startsGroup()}>
                      <div class="den-select__group" role="presentation">
                        {option.group}
                      </div>
                    </Show>
                    <div
                      id={optionId(index())}
                      class="den-select__option"
                      classList={{
                        "den-select__option--active": active() === index(),
                      }}
                      role="option"
                      aria-selected={currentValue() === option.value}
                      aria-disabled={disabled() || option.disabled || undefined}
                      data-option-index={index()}
                      data-value={option.value}
                      onPointerMove={() => {
                        if (!disabled() && !option.disabled) setActive(index());
                      }}
                      onPointerDown={(event) => event.preventDefault()}
                      onClick={() => pick(index())}
                    >
                      <span class="den-select__check" aria-hidden="true">
                        {currentValue() === option.value ? "✓" : ""}
                      </span>
                      <span class="den-select__option-copy">
                        <span class="den-select__option-label">{option.label}</span>
                        <Show when={option.description}>
                          <span class="den-select__option-description">
                            {option.description}
                          </span>
                        </Show>
                      </span>
                    </div>
                  </>
                );
              }}
            </For>
          </div>
          </div>
        </AnchoredSurface>
      </Show>
    </div>
  );
}
