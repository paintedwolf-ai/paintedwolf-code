import { Show } from "solid-js";
import { generatingTokensLabel } from "../../chat/transcript/projection/generating-tokens.ts";

/** Minimal working spinner for the worker activity pane. */
export function WorkerWorkingIndicator(props: { generatingTokens?: number }) {
  return (
    <div
      class="den-worker-working-indicator"
      role="status"
      aria-live="polite"
      aria-busy="true"
      aria-label="Working"
      data-testid="worker-working-indicator"
    >
      <span class="den-worker-working-indicator-spinner" aria-hidden="true" />
      <Show when={generatingTokensLabel(props.generatingTokens)}>
        {(label) => (
          <span
            class="den-worker-working-indicator-generating"
            data-testid="worker-working-generating"
          >
            {label()}
          </span>
        )}
      </Show>
    </div>
  );
}
