import { Show } from "solid-js";
import type { AttentionRow } from "../../api/types.ts";
import { marksHiddenPane } from "../../attention/attention-model.ts";

/** Attention worth marking on a pane the person cannot currently see. */
export function paneAttentionMark(
  attention: AttentionRow | undefined,
): AttentionRow | undefined {
  return attention && marksHiddenPane(attention.class) ? attention : undefined;
}

/** Dot on a pane control; its class carries the tone that says which state. */
export function PaneAttentionDot(props: { row: AttentionRow | undefined }) {
  return (
    <Show when={props.row} keyed>
      {(row) => (
        <span
          class="den-pane-toggle__dot"
          data-attention-class={row.class}
          aria-hidden="true"
        />
      )}
    </Show>
  );
}
