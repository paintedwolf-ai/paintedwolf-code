import { CacheSavingsNote } from "../../cost/CacheSavingsNote.tsx";
import { CostAmount } from "../../cost/CostAmount.tsx";
import { Index, Show, type JSX } from "solid-js";
import type { CostSummary, TokenTotals } from "../../api/types.ts";
import {
  callCount,
  estimateView,
  formatTokenSplit,
  formatTokenSplitExact,
  formatTokensCompact,
  hostMeasuredTokens,
  sourceAsOfLabel,
  trackingSinceLabel,
  unreportedFreeCalls,
} from "../../cost/cost-format.ts";
import { costRoles } from "../../cost/cost-roles.ts";
import { formatNanoUsd } from "../../cost/nano-usd.ts";
import { ShowLatest } from "../primitives/ShowLatest.tsx";

/** In/out tokens for one breakdown row — rounded, exact counts on hover. */
function tokenNote(totals: TokenTotals): JSX.Element {
  return (
    <small data-tip={formatTokenSplitExact(totals)}>
      {formatTokenSplit(totals)}
    </small>
  );
}

type Props = {
  summary?: CostSummary;
  trackingSince?: string | null;
  loading?: boolean;
};

export function CostSummaryPanel(props: Props) {
  const summary = () => props.summary;

  return (
    <div class="den-cost-summary" data-testid="cost-summary">
      <Show when={props.trackingSince}>
        <p class="den-settings-hint" data-testid="cost-summary-tracking-since">
          {trackingSinceLabel(props.trackingSince)}
        </p>
      </Show>

      <ShowLatest
        when={summary()}
        fallback={
          <p class="den-settings-hint" role="status" data-testid="cost-summary-empty">
            {props.loading ? "Loading estimated cost…" : "No estimated cost for this chat yet."}
          </p>
        }
      >
        {(s) => {
          const view = () => estimateView(s());
          const unpriced = () => view().coverage === "unpriced";
          return (
            <>
              <p class="den-settings-hint" data-testid="cost-summary-disclaimer">
                Figures are estimated — not invoices.
              </p>
              <Show when={unpriced()}>
                <p
                  class="den-settings-warn"
                  role="status"
                  data-testid="cost-summary-unpriced"
                >
                  Pricing unavailable — tokens only
                </p>
              </Show>
              <Show when={view().coverage === "lower_bound"}>
                <p
                  class="den-settings-warn"
                  role="status"
                  data-testid="cost-summary-lower-bound"
                >
                  Lower bound — {view().reasons.join("; ")}. Actual charges may be higher.
                </p>
              </Show>
              <Show when={unreportedFreeCalls(s()) > 0}>
                <p
                  class="den-settings-hint"
                  role="status"
                  data-testid="cost-summary-unknown-local-calls"
                >
                  {callCount(unreportedFreeCalls(s()), "local")} — those tokens are missing from the counts, but local inference costs nothing
                </p>
              </Show>
              <Show when={hostMeasuredTokens(s()) > 0}>
                <p
                  class="den-settings-hint"
                  role="status"
                  data-testid="cost-summary-host-measured"
                >
                  {formatTokensCompact(hostMeasuredTokens(s()))} tokens were counted by the host after a call ended without a usage report — included above, and approximate
                </p>
              </Show>

              <p class="den-settings-hint" data-testid="cost-summary-cache">
                Reported cache use: {formatTokensCompact(s().token_totals.cache_read ?? 0)} read · {formatTokensCompact(s().token_totals.cache_write ?? 0)} written. Both are included in input tokens.
              </p>
              <CacheSavingsNote savings={s().cache_savings} totals={s().token_totals} />
              <dl class="den-cost-summary-grid" data-testid="cost-summary-totals">
                <div>
                  <dt>Total (estimated)</dt>
                  <dd>
                    <CostAmount summary={s()} unpricedText="—" testId="cost-summary-total-usd" />
                  </dd>
                  <dd data-testid="cost-summary-tokens">
                    {tokenNote(s().token_totals)}
                  </dd>
                </div>
                {/* Index, not For: fixed rows whose figures tick during a run. */}
                <Index each={costRoles(s())}>
                  {(role) => (
                    <div>
                      <dt>{role().label} (estimated)</dt>
                      <dd data-testid={`cost-summary-${role().id}-usd`}>
                        {unpriced() ? "—" : formatNanoUsd(role().nano_usd)}
                      </dd>
                      <dd data-testid={`cost-summary-${role().id}-tokens`}>
                        {tokenNote(role().totals)}
                      </dd>
                    </div>
                  )}
                </Index>
              </dl>

              <p class="den-settings-hint" data-testid="cost-summary-source">
                {sourceAsOfLabel(s())}
              </p>
            </>
          );
        }}
      </ShowLatest>
    </div>
  );
}
