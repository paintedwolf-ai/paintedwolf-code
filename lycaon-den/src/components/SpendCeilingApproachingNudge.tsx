import { Show, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type {
  CostSummary,
  SettingsLimitsResponse,
} from "../api/types.ts";
import { estimateView, formatUSD, joinReasons } from "../cost/cost-format.ts";
import {
  spendCeilingApproaching,
  spendCeilingReadout,
} from "../settings/budgets/spend-ceiling-readout.ts";
import { NANO_PER_USD } from "../cost/nano-usd.ts";
import { raiseEffectiveSpendCeiling, type SpendCeilingUpdate } from "../settings/budgets/spend-ceiling-actions.ts";
import { SystemNudge } from "./SystemNudge.tsx";

type Props = {
  client: LycaonClient;
  sessionId: string;
  projectId: string;
  /** Session cost summary from the cost store; undefined while loading. */
  summary: CostSummary | undefined;
  /** Backend-persisted limits; null until the chips have loaded them. */
  limits: SettingsLimitsResponse | null;
  /** Keep the settings store in sync after Raise ceiling. */
  onLimitsUpdated?: (update: SpendCeilingUpdate) => void;
  /** Navigate to Settings → Advanced → Budgets. */
  onOpenBudgets: () => void;
};

// Dismissals expire when the app closes.
const dismissed = new Set<string>(); // `${sessionId}:${ceilingUsd}`
const [dismissTick, setDismissTick] = createSignal(0);

function dismissKey(sessionId: string, ceilingUsd: number): string {
  return `${sessionId}:${ceilingUsd}`;
}

function isDismissed(sessionId: string, ceilingUsd: number): boolean {
  dismissTick();
  return dismissed.has(dismissKey(sessionId, ceilingUsd));
}

function dismissFor(sessionId: string, ceilingUsd: number): void {
  dismissed.add(dismissKey(sessionId, ceilingUsd));
  setDismissTick((n) => n + 1);
}

/** Test isolation. */
export function resetSpendCeilingApproachingDismissalsForTest(): void {
  dismissed.clear();
  setDismissTick((n) => n + 1);
}

/** Reports whether this session should show the warning. */
export function spendCeilingApproachingVisible(args: {
  sessionId: string;
  limits: SettingsLimitsResponse | null;
  summary: CostSummary | undefined;
}): boolean {
  const limits = args.limits;
  if (!limits) return false;
  const ceilingUsd = (limits.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
  const readout = spendCeilingReadout({
    enabled: limits.spend_ceiling_enabled === true,
    ceilingUsd,
    warningRatio: limits.spend_warning_ratio,
    summary: args.summary,
  });
  if (!spendCeilingApproaching(readout)) return false;
  if (ceilingUsd <= 0) return false;
  return !isDismissed(args.sessionId, ceilingUsd);
}

/** Shows the warning before the ceiling is reached. */
export function SpendCeilingApproachingNudge(props: Props) {
  const [busy, setBusy] = createSignal(false);
  const [failed, setFailed] = createSignal(false);

  const visible = createMemo(() =>
    spendCeilingApproachingVisible({
      sessionId: props.sessionId,
      limits: props.limits,
      summary: props.summary,
    }),
  );

  const description = createMemo(() => {
    const ceiling = (props.limits?.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
    const landing = props.limits?.spend_soft_stop ?? true
      ? "At the ceiling it winds down work already in flight, then pauses until you raise it."
      : "At the ceiling it closes out and pauses until you raise it.";
    const view = estimateView(props.summary);
    const disclosure =
      view.coverage === "lower_bound"
        ? ` That is a lower bound: ${joinReasons(view.reasons)}.`
        : "";
    return `This session has spent ${view.phrase} of its ${formatUSD(ceiling)} ceiling.${disclosure} ${landing}`;
  });

  const raiseCeiling = async () => {
    const limits = props.limits;
    if (!limits || busy()) return;
    setBusy(true);
    setFailed(false);
    try {
      const spent = (props.summary?.estimated_nano_usd ?? 0) / NANO_PER_USD;
      const current = (limits.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
      const updated = await raiseEffectiveSpendCeiling(props.client, props.projectId, spent);
      dismissFor(props.sessionId, current);
      props.onLimitsUpdated?.(updated);
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Show when={visible()}>
      <SystemNudge
        testId="spend-ceiling-approaching-nudge"
        title="Approaching the spend ceiling"
        description={<>{description()}<Show when={failed()}><div role="alert">Could not raise the ceiling. Try again or open budgets.</div></Show></>}
        primaryAction={{
          label: busy() ? "Raising…" : "Raise ceiling",
          onClick: () => void raiseCeiling(),
          disabled: busy(),
        }}
        secondaryAction={{
          label: "Open budgets",
          onClick: () => props.onOpenBudgets(),
          disabled: busy(),
        }}
        onDismiss={() => {
          const ceiling = (props.limits?.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
          if (ceiling > 0) dismissFor(props.sessionId, ceiling);
        }}
      />
    </Show>
  );
}
