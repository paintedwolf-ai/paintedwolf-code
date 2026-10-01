import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { For, Show, createEffect, createSignal } from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import type { ModelRef, ProviderMeta } from "../../api/types.ts";
import {
  modelAssignmentOptionCount,
  modelAssignmentOptionGroupsForPicker,
  modelRefKey,
  parseModelRefKey,
} from "../../settings/providers/models-editor-model.ts";
import type { ModelRoleSlot } from "../../settings/providers/models-role-filter.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenField } from "../primitives/DenField.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { cn } from "../../shared/cn.ts";

type Props = {
  label: string;
  hint?: string;
  compact?: boolean;
  disabled?: boolean;
  /** Label for the empty assignment. */
  emptyLabel?: string;
  providers: readonly ProviderMeta[];
  /** Role used for exclusion filtering. */
  roleSlot: ModelRoleSlot;
  showAllModels?: boolean;
  value: ModelRef;
  onChange: (ref: ModelRef) => void;
  "data-testid"?: string;
  id?: string;
};

/** Searchable model assignment picker. */
export function ModelSlotPicker(props: Props) {
  const [open, setOpen] = createSignal(false);
  const [query, setQuery] = createSignal("");
  let filterRef: HTMLInputElement | undefined;
  let dialogRef: HTMLDivElement | undefined;

  const optionGroups = () =>
    modelAssignmentOptionGroupsForPicker(props.providers, props.value, {
      slot: props.roleSlot,
      showAll: props.showAllModels === true,
    });
  const optionCount = () => modelAssignmentOptionCount(optionGroups());
  /** Distinguishes role filtering from an empty catalog. */
  const allFilteredOutForRole = () =>
    optionCount() === 0 &&
    modelAssignmentOptionCount(
      modelAssignmentOptionGroupsForPicker(props.providers, props.value, {
        slot: props.roleSlot,
        showAll: true,
      }),
    ) > 0;
  const selectedKey = () => {
    if (!props.value.provider_id || !props.value.model) return "";
    return modelRefKey(props.value);
  };
  const selectedOption = () => {
    const key = selectedKey();
    if (!key) return null;
    for (const group of optionGroups()) {
      for (const option of group.options) {
        if (modelRefKey(option) === key) return option;
      }
    }
    return null;
  };
  const emptyOptionLabel = () => {
    if (allFilteredOutForRole())
      return MODELS_SETTINGS_COPY.allModelsFilteredForRole;
    if (optionCount() === 0) return MODELS_SETTINGS_COPY.selectModelFirst;
    return props.emptyLabel ?? MODELS_SETTINGS_COPY.unsetModel;
  };
  const filteredGroups = () => {
    const q = query().trim().toLowerCase();
    if (!q) return optionGroups();
    return optionGroups()
      .map((group) => ({
        ...group,
        options: group.options.filter(
          (option) =>
            option.title.toLowerCase().includes(q) ||
            (option.rates?.toLowerCase().includes(q) ?? false) ||
            option.provider_id.toLowerCase().includes(q) ||
            option.model.toLowerCase().includes(q),
        ),
      }))
      .filter((group) => group.options.length > 0);
  };
  // Role-filtered empty states remain explainable.
  const disabled = () =>
    props.disabled || (optionCount() === 0 && !props.value.provider_id && !allFilteredOutForRole());

  const close = () => {
    setOpen(false);
    setQuery("");
  };

  createModalFocusTrap(open, () => dialogRef, {
    onEscape: close,
  });

  const pick = (key: string) => {
    if (disabled()) return;
    if (key === "") {
      props.onChange({ provider_id: "", model: "" });
      close();
      return;
    }
    const ref = parseModelRefKey(key);
    if (ref) props.onChange(ref);
    close();
  };

  createEffect(() => {
    if (!open()) return;
    queueMicrotask(() => filterRef?.focus());
  });

  const trigger = (
    <button
      type="button"
      id={props.id}
      class={cn(
        "den-model-slot-trigger",
        props.compact ? "w-full" : "w-full max-w-[480px]",
      )}
      data-testid={props["data-testid"] ?? "model-slot-picker"}
      data-value={selectedKey()}
      aria-label={props.label}
      aria-haspopup="dialog"
      aria-expanded={open()}
      disabled={disabled()}
      onClick={() => {
        if (disabled()) return;
        setQuery("");
        setOpen(true);
      }}
    >
      <span class="den-model-slot-trigger__label">
        <Show
          when={selectedOption()}
          fallback={emptyOptionLabel()}
        >
          {(option) => (
            <>
              <span class="den-model-slot-trigger__title">{option().title}</span>
              <Show when={option().rates}>
                {(rates) => (
                  <span class="den-model-slot-rates">{rates()}</span>
                )}
              </Show>
            </>
          )}
        </Show>
      </span>
    </button>
  );

  return (
    <>
      <Show
        when={props.compact}
        fallback={
          <DenField class="den-settings-slot-single">
            <span class="den-settings-slot-label">{props.label}</span>
            <Show when={props.hint} fallback={null}>
              <span class="den-settings-hint">{props.hint}</span>
            </Show>
            {trigger}
          </DenField>
        }
      >
        {trigger}
      </Show>

      <Show when={open()}>
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop den-dialog-backdrop--viewport"
            data-testid={`${props["data-testid"] ?? "model-slot-picker"}-dialog`}
            onClick={(e) => {
              if (e.target === e.currentTarget) close();
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={dialogRef}
              class="den-dialog den-dialog--sheet"
              role="dialog"
              aria-modal="true"
              aria-labelledby={`${props["data-testid"] ?? "model-slot-picker"}-title`}
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2
                  id={`${props["data-testid"] ?? "model-slot-picker"}-title`}
                >
                  {props.label}
                </h2>
              </header>
              <DenInput
                ref={(el) => {
                  filterRef = el;
                }}
                class="den-settings-add-dialog__filter"
                type="search"
                autocomplete="off"
                data-testid={`${props["data-testid"] ?? "model-slot-picker"}-filter`}
                placeholder={MODELS_SETTINGS_COPY.filterModels}
                value={query()}
                onInput={(e) => setQuery(e.currentTarget.value)}
              />
              <Show
                when={filteredGroups().length > 0 || optionCount() > 0}
                fallback={
                  <p class="den-dialog__hint">
                    {allFilteredOutForRole()
                      ? MODELS_SETTINGS_COPY.allModelsFilteredForRoleHint
                      : MODELS_SETTINGS_COPY.selectModelFirst}
                  </p>
                }
              >
                <Scrollport
                  class="den-dialog__list"
                  contentAs="ul"
                  contentClass="den-dialog__list-content"
                  eager
                  data-testid={`${props["data-testid"] ?? "model-slot-picker"}-list`}
                >
                  <Show when={optionCount() > 0}>
                    <li>
                      <button
                        type="button"
                        class="den-dialog__row"
                        data-testid={`${props["data-testid"] ?? "model-slot-picker"}-unset`}
                        disabled={disabled()}
                        onClick={() => pick("")}
                      >
                        <span class="den-dialog__row-title">
                          {emptyOptionLabel()}
                        </span>
                      </button>
                    </li>
                  </Show>
                  <Show
                    when={filteredGroups().length > 0}
                    fallback={
                      <li>
                        <p class="den-dialog__hint">
                          {MODELS_SETTINGS_COPY.noModelFilterMatches}
                        </p>
                      </li>
                    }
                  >
                    <For each={filteredGroups()}>
                      {(group) => (
                        <>
                          <li class="den-settings-add-dialog__group">
                            {group.label}
                          </li>
                          <For each={group.options}>
                            {(option) => (
                              <li>
                                <button
                                  type="button"
                                  class="den-dialog__row"
                                  data-testid={`${props["data-testid"] ?? "model-slot-picker"}-option-${option.provider_id}-${option.model}`}
                                  aria-pressed={
                                    modelRefKey(option) === selectedKey()
                                  }
                                  disabled={disabled() || option.disabled}
                                  onClick={() => pick(modelRefKey(option))}
                                >
                                  <span class="den-settings-slot-option__head">
                                    <span class="den-dialog__row-title">
                                      {option.title}
                                    </span>
                                    <Show when={option.rates}>
                                      {(rates) => (
                                        <span class="den-model-slot-rates">
                                          {rates()}
                                        </span>
                                      )}
                                    </Show>
                                  </span>
                                  <Show when={option.disabled || option.unverified}><span class="den-settings-hint">{option.reason}</span></Show>
                                </button>
                              </li>
                            )}
                          </For>
                        </>
                      )}
                    </For>
                  </Show>
                </Scrollport>
              </Show>
              <footer class="den-settings-add-dialog__footer">
                <DenButton variant="secondary" compact onClick={() => close()}>
                  {MODELS_SETTINGS_COPY.cancel}
                </DenButton>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      </Show>
    </>
  );
}
