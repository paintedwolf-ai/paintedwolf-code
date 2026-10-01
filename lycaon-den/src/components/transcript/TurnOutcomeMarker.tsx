import { Match, Show, Switch } from "solid-js";
import type { SessionIdleDisposition } from "../../api/types.ts";

type Props = {
  /** Machine-derived session-idle disposition. */
  disposition: SessionIdleDisposition | undefined;
  /** Whether work happened in the turn before stopping. */
  hasProgress?: boolean;
  /** Retry callback for failed turns. */
  onRetry?: () => void;
  /** Continuation callback for turns with partial progress. */
  onKeepGoing?: () => void;
  /** Rewind-and-retry callback for turns with partial progress. */
  onRewindAndRetry?: () => void;
};

/** Render the machine-derived outcome of a settled turn. */
export function TurnOutcomeMarker(props: Props) {
  return (
    <Switch>
      <Match when={props.disposition === "user_stopped"}>
        <p class="den-turn-outcome den-turn-outcome--stopped" data-testid="turn-stopped-marker">
          Stopped
        </p>
      </Match>
      <Match when={props.disposition === "interrupted"}>
        <div
          class="den-turn-outcome den-turn-outcome--interrupted"
          data-testid="turn-interrupted-row"
        >
          <span>This turn was interrupted.</span>
          <Show
            when={props.hasProgress && (props.onKeepGoing || props.onRewindAndRetry)}
            fallback={
              <Show when={props.onRetry}>
                <button
                  type="button"
                  class="den-turn-outcome__retry"
                  data-testid="turn-interrupted-retry"
                  onClick={() => props.onRetry?.()}
                >
                  Retry
                </button>
              </Show>
            }
          >
            <Show when={props.onKeepGoing}>
              <button
                type="button"
                class="den-turn-outcome__retry"
                data-testid="turn-interrupted-keep-going"
                onClick={() => props.onKeepGoing?.()}
              >
                Keep going
              </button>
            </Show>
            <Show when={props.onRewindAndRetry}>
              <button
                type="button"
                class="den-turn-outcome__retry den-turn-outcome__retry--secondary"
                data-testid="turn-interrupted-rewind-and-retry"
                onClick={() => props.onRewindAndRetry?.()}
              >
                Rewind and retry
              </button>
            </Show>
          </Show>
        </div>
      </Match>
      <Match when={props.disposition === "turn_error"}>
        <div
          class="den-turn-outcome den-turn-outcome--error"
          role="alert"
          data-testid="turn-error-row"
        >
          <span>That turn didn't finish.</span>
          <Show
            when={props.hasProgress && (props.onKeepGoing || props.onRewindAndRetry)}
            fallback={
              <Show when={props.onRetry}>
                <button
                  type="button"
                  class="den-turn-outcome__retry"
                  data-testid="turn-error-retry"
                  onClick={() => props.onRetry?.()}
                >
                  Retry
                </button>
              </Show>
            }
          >
            <Show when={props.onKeepGoing}>
              <button
                type="button"
                class="den-turn-outcome__retry"
                data-testid="turn-error-keep-going"
                onClick={() => props.onKeepGoing?.()}
              >
                Keep going
              </button>
            </Show>
            <Show when={props.onRewindAndRetry}>
              <button
                type="button"
                class="den-turn-outcome__retry den-turn-outcome__retry--secondary"
                data-testid="turn-error-rewind-and-retry"
                onClick={() => props.onRewindAndRetry?.()}
              >
                Rewind and retry
              </button>
            </Show>
          </Show>
        </div>
      </Match>
    </Switch>
  );
}
