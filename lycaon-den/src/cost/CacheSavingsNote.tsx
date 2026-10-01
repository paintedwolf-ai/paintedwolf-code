import { Show } from "solid-js";
import type { CacheSavings, TokenTotals } from "../api/types.ts";
import { formatTokens } from "./cost-format.ts";
import { formatNanoUsd } from "./nano-usd.ts";

export function CacheSavingsNote(props: { savings?: CacheSavings; totals: TokenTotals }) {
  return (
    <Show when={props.savings}>
      {(savings) => (
        <p class="den-settings-hint" data-testid="cost-cache-comparison">
          <Show
            when={(props.totals.cache_read ?? 0) + (props.totals.cache_write ?? 0) > savings().unpriced_tokens}
            fallback={<>Cache comparison unavailable: reported cache tokens have no comparison price.</>}
          >
            Estimated cache comparison: {formatNanoUsd(Math.abs(savings().estimated_nano_usd))}{" "}
            {savings().estimated_nano_usd < 0 ? "more" : "less"} than ordinary input pricing
            for the same tokens, including cache-write premiums.
            <Show when={savings().unpriced_tokens > 0}>
              {" "}Partial comparison: {formatTokens(savings().unpriced_tokens)} cache
              tokens have no comparison price.
            </Show>
          </Show>
        </p>
      )}
    </Show>
  );
}
