import { sessionTitle } from "../../chat/session/session-title.ts";
import { For, Show, createMemo } from "solid-js";
import type { AttentionRow } from "../../api/types.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import {
  attentionAgeLabel,
  attentionReasonLabel,
  blockingRows,
} from "../../attention/attention-model.ts";

/** Cap on the band; Crossbar and the project sidebar carry larger lists. */
export const ATTENTION_BAND_CAP = 4;

export type AttentionBandProps = {
  rows: readonly AttentionRow[];
  /** Injected so the age labels are stable under test rather than wall-clock. */
  nowMs?: number;
  onOpen: (row: AttentionRow) => void;
};

/**
 * Cross-project "waiting on you" band above the home surface.
 * Shows only blocked chats; renders nothing when no chats are blocked.
 */
export function AttentionBand(props: AttentionBandProps) {
  const blocked = createMemo(() => blockingRows([...props.rows]));
  const shown = createMemo(() => blocked().slice(0, ATTENTION_BAND_CAP));
  const hidden = createMemo(() => blocked().length - shown().length);
  const now = () => props.nowMs ?? Date.now();

  return (
    <Show when={blocked().length > 0}>
      <section
        class="attention-band"
        data-testid="attention-band"
        aria-label="Chats waiting on you"
      >
        <div class="attention-band__head" {...chromeProps()}>
          <span class="attention-band__title">
            {blocked().length === 1
              ? "1 chat is waiting on you"
              : `${blocked().length} chats are waiting on you`}
          </span>
        </div>
        <ul class="attention-band__list">
          <For each={shown()}>
            {(row) => (
              <li>
                <button
                  type="button"
                  class="attention-band__row"
                  data-testid="attention-band-row"
                  data-session-id={row.session_id}
                  // The visible row reads as columns; spoken, it needs to be a
                  // sentence, and it must name the project the chat lives in.
                  aria-label={`${sessionTitle(row.title)}${
                    row.project_name?.trim() ? ` in ${row.project_name.trim()}` : ""
                  } — ${attentionReasonLabel(row)}`}
                  onClick={() => props.onOpen(row)}
                >
                  <span
                    class="den-attention-chip__dot attention-band__dot"
                    data-attention-class={row.class}
                    aria-hidden="true"
                  />
                  <span class="attention-band__primary">
                    {sessionTitle(row.title)}
                  </span>
                  {/* Distinguishes rows outside the current project. */}
                  <Show when={row.project_name?.trim()}>
                    {(name) => (
                      <span class="attention-band__project">{name()}</span>
                    )}
                  </Show>
                  <span class="attention-band__reason">
                    {attentionReasonLabel(row)}
                  </span>
                  <Show when={attentionAgeLabel(row, now())}>
                    {(age) => <span class="attention-band__age">{age()}</span>}
                  </Show>
                </button>
              </li>
            )}
          </For>
        </ul>
        {/* A capped list states how many rows it hides. */}
        <Show when={hidden() > 0}>
          <p class="attention-band__more" data-testid="attention-band-more">
            {`and ${hidden()} more`}
          </p>
        </Show>
      </section>
    </Show>
  );
}
