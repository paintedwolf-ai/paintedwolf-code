import { For, Show, createMemo, createSignal, onMount } from "solid-js";
import type { ExtensionSuggestion, ExtensionSuggestionsResponse } from "../../api/types.ts";
import { EXTENSION_SUGGESTIONS_COPY } from "../../settings/extensions/extension-suggestions-copy.ts";
import {
  extensionSuggestionDetail,
  installableExtensionSuggestions,
} from "../../settings/extensions/extension-suggestions-model.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";

const INLINE_LIMIT = 4;

type Props = {
  response: ExtensionSuggestionsResponse;
  onSelectionChange: (ids: string[]) => void;
};

export function ExtensionSuggestionsBlock(props: Props) {
  const suggestions = createMemo(() => installableExtensionSuggestions(props.response));
  const [selected, setSelected] = createSignal<ReadonlySet<string>>(new Set());
  const [expanded, setExpanded] = createSignal(false);

  onMount(() => props.onSelectionChange([]));

  const shown = () =>
    expanded() ? suggestions() : suggestions().slice(0, INLINE_LIMIT);
  const hidden = () => Math.max(0, suggestions().length - INLINE_LIMIT);
  const allSelected = () =>
    suggestions().length > 0 && selected().size === suggestions().length;

  const emit = (next: ReadonlySet<string>) => {
    setSelected(next);
    props.onSelectionChange([...next]);
  };

  const toggle = (id: string, on: boolean) => {
    const next = new Set(selected());
    if (on) next.add(id);
    else next.delete(id);
    emit(next);
  };

  const toggleAll = () => {
    emit(
      allSelected()
        ? new Set<string>()
        : new Set(suggestions().map((suggestion) => suggestion.id)),
    );
  };

  return (
    <Show when={suggestions().length > 0}>
      <div class="extension-suggestions" data-testid="extension-suggestions-block">
        <div class="extension-suggestions__group-title extension-suggestions__group-title--split">
          <span data-testid="extension-suggestions-title">
            {EXTENSION_SUGGESTIONS_COPY.title(suggestions().length)}
          </span>
          <span class="extension-suggestions__scope">
            <Show when={suggestions().length > 1}>
              <span
                class="extension-suggestions__count-pill"
                data-empty={selected().size === 0 ? "true" : undefined}
                data-testid="extension-suggestions-selected"
              >
                {EXTENSION_SUGGESTIONS_COPY.selectedCount(selected().size)}
              </span>
              <button
                type="button"
                class="extension-suggestions__select-all"
                data-testid="extension-suggestions-select-all"
                onClick={toggleAll}
              >
                {allSelected()
                  ? EXTENSION_SUGGESTIONS_COPY.selectNone
                  : EXTENSION_SUGGESTIONS_COPY.selectAll}
              </button>
            </Show>
            <Show when={suggestions().length <= 1}>
              {EXTENSION_SUGGESTIONS_COPY.scope}
            </Show>
          </span>
        </div>

        <ul class="extension-suggestions__list">
          <For each={shown()}>
            {(suggestion) => (
              <SuggestionRow
                suggestion={suggestion}
                checked={selected().has(suggestion.id)}
                onToggle={toggle}
              />
            )}
          </For>
        </ul>

        <Show when={hidden() > 0 && !expanded()}>
          <button
            type="button"
            class="extension-suggestions__show-all"
            data-testid="extension-suggestions-show-all"
            onClick={() => setExpanded(true)}
          >
            {EXTENSION_SUGGESTIONS_COPY.showAll(suggestions().length)}
          </button>
        </Show>

        <p class="extension-suggestions__install-hint">
          {EXTENSION_SUGGESTIONS_COPY.foot} {EXTENSION_SUGGESTIONS_COPY.skipNote}
        </p>
      </div>
    </Show>
  );
}

function SuggestionRow(props: {
  suggestion: ExtensionSuggestion;
  checked: boolean;
  onToggle: (id: string, on: boolean) => void;
}) {
  const detail = () => extensionSuggestionDetail(props.suggestion);
  return (
    <li
      class="extension-suggestions__row"
      data-testid={`extension-suggestions-row-${props.suggestion.id}`}
    >
      <DenCheckbox
        class="extension-suggestions__check"
        checked={props.checked}
        data-testid={`extension-suggestions-toggle-${props.suggestion.id}`}
        onChange={(event) =>
          props.onToggle(props.suggestion.id, event.currentTarget.checked)
        }
      >
        <span class="sr-only">{props.suggestion.id}</span>
      </DenCheckbox>
      <span class="extension-suggestions__name">{props.suggestion.id}</span>
      <Show when={detail()}>
        <span class="extension-suggestions__detail">{detail()}</span>
      </Show>
    </li>
  );
}
